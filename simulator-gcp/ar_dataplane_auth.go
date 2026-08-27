package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Authentication for the Google Artifact Registry Docker data plane — the
// Docker Registry HTTP API v2 surface under /v2/, which Artifact Registry
// serves at `<location>-docker.pkg.dev` and which implements version 1.1 of the
// OCI Distribution Specification ("Support for the Docker Registry API",
// Artifact Registry documentation).
//
// Artifact Registry's contract is its own, and differs from the other
// registries the shared /v2/ library serves in three ways that this file
// implements literally:
//
//  1. The refusal of a request that presented NO credential carries the Docker
//     Registry v2 Bearer challenge; the refusal of one that presented a
//     credential the registry could not accept carries NO challenge at all.
//  2. A request that authenticated but whose principal cannot reach the
//     repository is refused with HTTP 403 and the `DENIED` code naming the IAM
//     permission and the repository resource — not with a 401
//     `insufficient_scope` challenge.
//  3. The token the challenge's realm issues carries the caller's identity,
//     not the requested scope: authorization is evaluated per request against
//     the repository the request addresses.
//
// The wire shapes below were taken from the live service. Each is quoted at
// the constant or function that reproduces it.

// arTokenPath is the Docker token service Artifact Registry names in the
// `realm` of every challenge it sends. Against the live service:
//
//	$ curl -i https://us-central1-docker.pkg.dev/v2/
//	HTTP/2 401
//	www-authenticate: Bearer realm="https://us-central1-docker.pkg.dev/v2/token"
//
// The realm is the registry's own origin plus this path, so a client pointed at
// the simulator by coordinate follows the challenge back to the simulator.
const arTokenPath = "/v2/token"

// arRegistryTokenTTL is how long a token from the Docker token service stays
// valid. Artifact Registry returns `"expires_in":43200` — twelve hours:
//
//	$ curl 'https://us-docker.pkg.dev/v2/token?service=us-docker.pkg.dev&scope=repository:cloudrun/container/hello:pull'
//	{"token":"…","expires_in":43200}
const arRegistryTokenTTL = 12 * time.Hour

// arRegistryTokenProduct is the `product` member of the token envelope the
// Docker token service issues, as the live service spells it. Base64-decoding
// a token from the command above yields:
//
//	{"access_token":"ABmYrfDZZ6Yt…","commands":{"cloudrun/container/hello":{"14":true}},"token_id":4268243893102209432,"product":"artifactregistry"}
const arRegistryTokenProduct = "artifactregistry"

// The Docker Registry v2 usernames Artifact Registry recognises for an HTTP
// Basic credential, from "Authenticate Docker to Artifact Registry":
// `oauth2accesstoken` carries an OAuth 2.0 access token as the password,
// `_json_key` the service-account key file verbatim, and `_json_key_base64`
// that same file base64-encoded. The gcloud CLI credential helper and the
// standalone `docker-credential-gcr` helper both produce the first of these,
// so configuring either reaches the same code path.
const (
	arUserAccessToken   = "oauth2accesstoken"
	arUserJSONKey       = "_json_key"
	arUserJSONKeyBase64 = "_json_key_base64"
)

// The IAM permissions Artifact Registry checks for each Docker Registry v2
// method, from the method/permission table in "Artifact Registry audit
// logging": Docker-GetManifest, Docker-ServeBlob, Docker-GetTags and
// Docker-Catalog require downloadArtifacts; Docker-PutManifest,
// Docker-StartUpload, Docker-FinishUpload and Docker-CancelUpload require
// uploadArtifacts; Docker-DeleteManifest and Docker-DeleteBlob require
// deleteArtifacts.
const (
	arPermDownload = "artifactregistry.repositories.downloadArtifacts"
	arPermUpload   = "artifactregistry.repositories.uploadArtifacts"
	arPermDelete   = "artifactregistry.repositories.deleteArtifacts"
)

// arDefaultImageComponent is the image path component Artifact Registry
// substitutes when a /v2/ path names a project and a repository but no image.
// The live service reports the scope it wants with that component filled in:
//
//	$ curl -i https://us-central1-docker.pkg.dev/v2/foo/bar/manifests/latest
//	www-authenticate: … scope="repository:foo/bar/pkg:pull"
const arDefaultImageComponent = "pkg"

// arPrincipal is the identity behind a data-plane request. An anonymous
// principal is one that reached the data plane with a token the Docker token
// service issued without a credential — Artifact Registry issues those, and
// refuses them at the resource.
type arPrincipal struct {
	subject   string
	anonymous bool
}

// arAuthorizeV2 is the OCIRegistry.Authorize hook the Artifact Registry data
// plane mounts. It runs after the shared registry has parsed the repository out
// of the /v2/ route and before any handler touches a store.
func arAuthorizeV2(w http.ResponseWriter, r *http.Request, repo string) bool {
	// A /v2/ path with a single component names no repository at all, and
	// Artifact Registry rejects its shape before it looks at any credential:
	//
	//	$ curl -i https://us-central1-docker.pkg.dev/v2/foo/manifests/latest
	//	HTTP/2 400
	//	{"errors":[{"code":"NAME_INVALID","message":"invalid number of path components: 1"}]}
	if repo != "" {
		if components := strings.Split(repo, "/"); len(components) < 2 {
			arRegistryError(w, http.StatusBadRequest, "NAME_INVALID",
				fmt.Sprintf("invalid number of path components: %d", len(components)))
			return false
		}
	}

	principal, presented, err := arAuthenticate(r)
	switch {
	case !presented:
		arChallenge(w, r, repo)
		return false
	case err != nil:
		// A credential was presented and the registry could not accept it. The
		// live service answers this WITHOUT a challenge header — the client
		// already knows where the token service is, and repeating the
		// challenge would send it back to mint the same rejected credential:
		//
		//	$ curl -i -H 'Authorization: Bearer notarealtoken' \
		//	    https://us-central1-docker.pkg.dev/v2/my-project/my-repo/my-image/manifests/latest
		//	HTTP/2 401
		//	{"errors":[{"code":"UNAUTHORIZED","message":"No valid credential was supplied."}]}
		//
		// The same response answers an unparseable Basic credential, an
		// unknown Basic username, an expired token, and an Authorization header
		// whose scheme the registry does not implement.
		arRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "No valid credential was supplied.")
		return false
	}

	// The base endpoint addresses the registry rather than a repository, and
	// needs a credential the registry accepts and nothing more — including one
	// the token service issued without any credential of its own:
	//
	//	$ ANON=$(curl -s '…/v2/token?service=…&scope=…' | jq -r .token)
	//	$ curl -i -H "Authorization: Bearer $ANON" https://us-docker.pkg.dev/v2/
	//	HTTP/2 200
	//	docker-distribution-api-version: registry/2.0
	//
	// This is the endpoint a client probes to discover both API support and the
	// token service, so a registry that refused an anonymous token here would
	// never let a client reach the challenge that tells it what to do next.
	if repo == "" {
		return true
	}

	permission := arPermissionForMethod(r.Method)
	resource, located := arRepositoryResource(r, repo)
	if !located {
		// The request named a repository that no location of this registry
		// holds, at a coordinate that names no location either, so there is no
		// Artifact Registry resource to report a verdict about — and the
		// simulator will not invent a location to name one. The registry
		// answers with the OCI Distribution code for a repository it does not
		// know, which Artifact Registry's own /v2/ surface implements.
		arRegistryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return false
	}
	if principal.anonymous || !arRepositoryExists(r, repo) {
		arPermissionDenied(w, principal, permission, resource)
		return false
	}
	return true
}

// arPermissionForMethod maps a Docker Registry v2 request onto the IAM
// permission Artifact Registry checks for it, following the audit-logging
// method table: a read downloads, a removal deletes, and every upload verb
// (the POST that starts one, the PATCH that appends to it, the PUT that
// finishes it, and the PUT of a manifest) uploads.
func arPermissionForMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead:
		return arPermDownload
	case http.MethodDelete:
		return arPermDelete
	default:
		return arPermUpload
	}
}

// arChallengeActions renders the actions the Bearer challenge asks the client
// to obtain, in the Docker Registry v2 scope grammar. Artifact Registry asks
// for `pull` on a read and for the `pull,push` pair on everything that mutates
// — including a DELETE, whose IAM permission is deleteArtifacts but whose
// challenge names the write pair:
//
//	$ curl -i -X DELETE https://us-central1-docker.pkg.dev/v2/my-project/my-repo/my-image/manifests/sha256:00…
//	www-authenticate: … scope="repository:my-project/my-repo/my-image:pull,push"
func arChallengeActions(method string) string {
	if method == http.MethodGet || method == http.MethodHead {
		return "pull"
	}
	return "pull,push"
}

// arChallenge answers a request that presented no credential with the Docker
// Registry v2 Bearer challenge and Artifact Registry's 401 body.
//
// The two shapes below are both from the live service. A request that
// addresses the registry itself is told only where the token service is:
//
//	$ curl -i https://us-central1-docker.pkg.dev/v2/
//	www-authenticate: Bearer realm="https://us-central1-docker.pkg.dev/v2/token"
//	{"errors":[{"code":"UNAUTHORIZED","message":"not authenticated: No credential was supplied."}]}
//
// while one that addresses a repository is additionally told the service and
// the scope to ask for, and carries the longer message:
//
//	$ curl -i https://us-central1-docker.pkg.dev/v2/my-project/my-repo/my-image/manifests/latest
//	www-authenticate: Bearer realm="https://us-central1-docker.pkg.dev/v2/token",service="us-central1-docker.pkg.dev",scope="repository:my-project/my-repo/my-image:pull"
//	{"errors":[{"code":"UNAUTHORIZED","message":"not authenticated: No valid credential was supplied."}]}
func arChallenge(w http.ResponseWriter, r *http.Request, repo string) {
	realm := requestOrigin(r) + arTokenPath
	challenge := fmt.Sprintf("Bearer realm=%q", realm)
	message := "not authenticated: No credential was supplied."
	if repo != "" {
		challenge += fmt.Sprintf(",service=%q,scope=%q", r.Host, arChallengeScope(r, repo))
		message = "not authenticated: No valid credential was supplied."
	}
	w.Header().Set("WWW-Authenticate", challenge)
	arRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", message)
}

// arChallengeScope renders the token scope the challenge asks for. A path that
// names a project and a repository but no image is reported with Artifact
// Registry's own substituted image component.
func arChallengeScope(r *http.Request, repo string) string {
	if len(strings.Split(repo, "/")) == 2 {
		repo += "/" + arDefaultImageComponent
	}
	return "repository:" + repo + ":" + arChallengeActions(r.Method)
}

// arPermissionDenied answers a request whose credential authenticated but whose
// principal cannot reach the resource. Artifact Registry answers HTTP 403 with
// the Docker Registry v2 `DENIED` code, and the message names the IAM
// permission and the repository resource. An identified principal is told:
//
//	Permission "artifactregistry.repositories.downloadArtifacts" denied on resource "projects/xxx/locations/us/repositories/gcr.io" (or it may not exist)
//
// (reported verbatim by `docker pull` in Google's developer forums), while a
// principal that reached the data plane with an anonymous token from the token
// service is told so explicitly:
//
//	$ curl -X POST -H "Authorization: Bearer $ANON_TOKEN" \
//	    https://us-docker.pkg.dev/v2/cloudrun/container/hello/blobs/uploads/
//	HTTP/2 403
//	{"errors":[{"code":"DENIED","message":"Unauthenticated request. Unauthenticated requests do not have permission \"artifactregistry.repositories.uploadArtifacts\" on resource \"projects/cloudrun/locations/us/repositories/container\" (or it may not exist)"}]}
//
// The trailing "(or it may not exist)" is the service deliberately conflating
// a missing permission with a missing resource, so a caller cannot probe for
// the existence of a repository it cannot reach.
func arPermissionDenied(w http.ResponseWriter, principal arPrincipal, permission, resource string) {
	message := fmt.Sprintf("Permission %q denied on resource %q (or it may not exist)", permission, resource)
	if principal.anonymous {
		message = fmt.Sprintf("Unauthenticated request. Unauthenticated requests do not have permission %q on resource %q (or it may not exist)",
			permission, resource)
	}
	arRegistryError(w, http.StatusForbidden, "DENIED", message)
}

// arRegistryError writes the Docker Registry v2 error envelope Artifact
// Registry returns. Every /v2/ response the service sends carries the
// `Docker-Distribution-Api-Version` header, which the shared registry has
// already set on the response by the time the Authorize hook runs; the token
// service, which is not part of that subtree, sets it here.
func arRegistryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	sim.WriteJSON(w, status, map[string]any{
		"errors": []map[string]any{{"code": code, "message": message}},
	})
}

// arRegistryHostSuffix is the tail of every Artifact Registry Docker endpoint.
// The label in front of it is the location the endpoint serves, and it is the
// only place a /v2/ request carries one: the repository path holds the project,
// the repository and the image, never the region. The live service reads it
// straight back into the resource it names, for a repository that exists and
// for one that does not alike:
//
//	$ curl 'https://europe-west1-docker.pkg.dev/v2/token?service=europe-west1-docker.pkg.dev&scope=repository:no-such-project/no-such-repo/app:pull'
//	{"errors":[{"code":"DENIED","message":"Unauthenticated request. Unauthenticated requests do not have permission \"artifactregistry.repositories.downloadArtifacts\" on resource \"projects/no-such-project/locations/europe-west1/repositories/no-such-repo\" (or it may not exist)"}]}
//
// with `us-docker.pkg.dev` yielding `locations/us` and `asia-docker.pkg.dev`
// `locations/asia` for the identical path.
const arRegistryHostSuffix = "-docker.pkg.dev"

// arRequestLocation reports the Artifact Registry location the endpoint a
// request addressed serves, and whether that endpoint named one at all. A
// client that resolved a real registry coordinate names it in the `Host`
// header; the simulator's own coordinate is a bare address that names none.
func arRequestLocation(r *http.Request) (string, bool) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	location, ok := strings.CutSuffix(host, arRegistryHostSuffix)
	if !ok || location == "" {
		return "", false
	}
	return location, true
}

// arRepositoryResource renders the Artifact Registry repository resource a
// /v2/ image path addresses, in the `projects/{project}/locations/{location}/
// repositories/{repository}` form the service names in its denials, and
// reports whether the request identifies one at all.
//
// The project and repository come from the path's first two components — the
// layout Artifact Registry documents for a Docker image,
// `LOCATION-docker.pkg.dev/PROJECT/REPOSITORY/IMAGE`. The location comes from
// the registry endpoint, as it does against the real service. Reaching the
// simulator at its own coordinate names no location, and there the repository
// the control plane created supplies it — the one location this registry holds
// that repository at. When neither names one, the request identifies no
// resource, and none is manufactured.
func arRepositoryResource(r *http.Request, repo string) (string, bool) {
	parts := strings.SplitN(repo, "/", 3)
	if len(parts) < 2 {
		return "", false
	}
	project, repoID := parts[0], parts[1]
	location, ok := arRequestLocation(r)
	if !ok {
		location, ok = artifactRegistryRepositoryLocation(project, repoID)
	}
	if !ok {
		return "", false
	}
	return fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID), true
}

// arRepositoryExists reports whether the repository a /v2/ image path addresses
// was created through the Artifact Registry control plane at the location the
// request addressed. A registry request against a repository that does not
// exist is refused the same way one against a repository the caller cannot
// reach is — which is what "(or it may not exist)" in the denial message says.
func arRepositoryExists(r *http.Request, repo string) bool {
	resource, ok := arRepositoryResource(r, repo)
	if !ok || arRepos == nil {
		return false
	}
	_, exists := arRepos.Get(resource)
	return exists
}

// arAuthenticate resolves the identity behind a data-plane request. It reports
// whether a credential was presented at all — the distinction that decides
// between the challenge and the flat refusal — and, when one was, either the
// principal it establishes or the reason it was not accepted.
//
// Three credential carriers reach the Artifact Registry data plane, and every
// one of them is verified against material this simulator issued:
//
//   - `Authorization: Bearer <token>` carrying either a token from this
//     registry's own Docker token service or an OAuth 2.0 access token. Google
//     documents the latter directly: `curl -H "Authorization: Bearer $(gcloud
//     auth print-access-token)" "https://us-docker.pkg.dev/v2/my-project/
//     my-repo/my-image/tags/list"` ("Support for the Docker Registry API").
//   - `Authorization: Basic` with the `oauth2accesstoken` username and an
//     OAuth 2.0 access token as the password — what the gcloud CLI credential
//     helper, the standalone `docker-credential-gcr` helper, and the documented
//     `gcloud auth print-access-token | docker login -u oauth2accesstoken
//     --password-stdin` all present.
//   - `Authorization: Basic` with the `_json_key` or `_json_key_base64`
//     username and a service-account key file as the password.
func arAuthenticate(r *http.Request) (arPrincipal, bool, error) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if authorization == "" {
		return arPrincipal{}, false, nil
	}
	scheme, credential, _ := strings.Cut(authorization, " ")
	credential = strings.TrimSpace(credential)
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		principal, err := arPrincipalFromBearer(credential)
		return principal, true, err
	case strings.EqualFold(scheme, "Basic"):
		principal, err := arPrincipalFromBasic(credential)
		return principal, true, err
	default:
		return arPrincipal{}, true, fmt.Errorf("unsupported authorization scheme %q", scheme)
	}
}

// arPrincipalFromBearer resolves the identity a Bearer credential carries. The
// credential is either a token this registry's Docker token service issued —
// which a Docker client obtains by following the challenge — or a bare OAuth
// 2.0 access token, which Google's own documentation presents to /v2/ directly.
func arPrincipalFromBearer(credential string) (arPrincipal, error) {
	if envelope, ok := arDecodeRegistryToken(credential); ok {
		return envelope.principal()
	}
	return arPrincipalFromAccessToken(credential)
}

// arPrincipalFromAccessToken verifies an OAuth 2.0 access token this simulator
// minted — through the OAuth2 token endpoint, the Security Token Service, the
// GCE metadata server, or IAM Credentials — and names the principal it was
// minted for.
func arPrincipalFromAccessToken(token string) (arPrincipal, error) {
	claims, err := verifiedAccessTokenClaims(token)
	if err != nil {
		return arPrincipal{}, err
	}
	return arPrincipal{subject: claims.Sub}, nil
}

// arPrincipalFromBasic resolves the identity an HTTP Basic credential carries,
// dispatching on the username Artifact Registry reserves for each credential
// kind. A username the service does not reserve is not a credential it can
// accept.
func arPrincipalFromBasic(credential string) (arPrincipal, error) {
	raw, err := base64.StdEncoding.DecodeString(credential)
	if err != nil {
		return arPrincipal{}, fmt.Errorf("basic credential is not base64: %w", err)
	}
	username, password, ok := strings.Cut(string(raw), ":")
	if !ok {
		return arPrincipal{}, fmt.Errorf("basic credential is not user:password")
	}
	switch username {
	case arUserAccessToken:
		return arPrincipalFromAccessToken(password)
	case arUserJSONKey:
		return arPrincipalFromServiceAccountKey([]byte(password))
	case arUserJSONKeyBase64:
		decoded, err := base64.StdEncoding.DecodeString(password)
		if err != nil {
			return arPrincipal{}, fmt.Errorf("service-account key is not base64: %w", err)
		}
		return arPrincipalFromServiceAccountKey(decoded)
	default:
		return arPrincipal{}, fmt.Errorf("username %q is not an Artifact Registry credential", username)
	}
}

// arPrincipalFromServiceAccountKey verifies a service-account key file
// presented as the password of a `_json_key` credential. The file is the one
// the IAM CreateServiceAccountKey API returned, so verification is the same
// question the OAuth2 token endpoint asks of a JWT-bearer assertion — does this
// key belong to a registered, enabled service account? — answered directly
// rather than through a signature, because a `_json_key` credential hands the
// registry the private key itself: possession is proven by the presented
// private key being the private half of the public key the key's registration
// recorded.
func arPrincipalFromServiceAccountKey(keyFile []byte) (arPrincipal, error) {
	var key struct {
		Type         string `json:"type"`
		ClientEmail  string `json:"client_email"`
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
	}
	if err := json.Unmarshal(keyFile, &key); err != nil {
		return arPrincipal{}, fmt.Errorf("service-account key is not JSON: %w", err)
	}
	if key.Type != "service_account" {
		return arPrincipal{}, fmt.Errorf("credential type %q is not service_account", key.Type)
	}
	if key.ClientEmail == "" || key.PrivateKeyID == "" {
		return arPrincipal{}, fmt.Errorf("service-account key names no account or key")
	}

	saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", gcpProjectFromEmail(key.ClientEmail), key.ClientEmail)
	account, ok := iamServiceAccounts.Get(saName)
	if !ok {
		return arPrincipal{}, fmt.Errorf("service account %q is not registered", key.ClientEmail)
	}
	if account.Disabled {
		return arPrincipal{}, fmt.Errorf("service account %q is disabled", key.ClientEmail)
	}
	material, ok := iamSAKeyPublics.Get(saName + "/keys/" + key.PrivateKeyID)
	if !ok {
		return arPrincipal{}, fmt.Errorf("key %q is not registered for %q", key.PrivateKeyID, key.ClientEmail)
	}
	if err := arKeyPairMatches(key.PrivateKey, material.PublicKeyPEM); err != nil {
		return arPrincipal{}, err
	}
	return arPrincipal{subject: key.ClientEmail}, nil
}

// arKeyPairMatches reports whether a presented PKCS#8 private key is the
// private half of a registered PKIX public key. A key file whose private half
// was replaced — the shape a forged credential takes, since the account email
// and key id in it are not secret — fails here.
func arKeyPairMatches(privateKeyPEM, publicKeyPEM string) error {
	privBlock, _ := pem.Decode([]byte(privateKeyPEM))
	if privBlock == nil {
		return fmt.Errorf("service-account private key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(privBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse service-account private key: %w", err)
	}
	priv, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("service-account private key is not RSA")
	}
	pubBlock, _ := pem.Decode([]byte(publicKeyPEM))
	if pubBlock == nil {
		return fmt.Errorf("registered public key is not PEM")
	}
	registered, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse registered public key: %w", err)
	}
	if !priv.PublicKey.Equal(registered) {
		return fmt.Errorf("service-account private key does not match the registered key")
	}
	return nil
}

// arRegistryTokenEnvelope is the JSON the Docker token service base64-encodes
// into the opaque `token` it returns. The member names are Artifact Registry's
// own, read out of a live token (see arRegistryTokenProduct).
//
// AccessToken carries the identity: for a credentialled token request it is an
// OAuth 2.0 access token this simulator minted for the authenticated principal,
// which is what makes the returned registry token verifiable when it comes back
// as a Bearer. Artifact Registry issues a token for an uncredentialled request
// too, and that token carries an opaque internal handle for the anonymous
// caller; the simulator has no equivalent handle and does not fabricate one, so
// its anonymous token simply carries no access token — which is exactly what
// the data plane then refuses at the resource.
//
// The live envelope also carries a `commands` member holding the permissions
// the caller already holds on the requested scope, keyed by numeric permission
// identifiers Google does not publish. Those identifiers are unknown, so the
// simulator emits no `commands` member rather than invent them — and nothing is
// lost by that, because the map is a cache rather than the gate: a token minted
// for `repository:cloudrun/container/hello:pull` serves a request against
// `cloudrun/container/job` too, which means the service re-evaluates access per
// request against the repository addressed rather than restricting the token to
// the scope it was asked for.
type arRegistryTokenEnvelope struct {
	AccessToken string `json:"access_token,omitempty"`
	TokenID     int64  `json:"token_id"`
	Product     string `json:"product"`
}

// principal resolves the identity a registry token carries.
func (e arRegistryTokenEnvelope) principal() (arPrincipal, error) {
	if e.AccessToken == "" {
		return arPrincipal{anonymous: true}, nil
	}
	return arPrincipalFromAccessToken(e.AccessToken)
}

// arDecodeRegistryToken reports whether a Bearer credential is one of this
// registry's own token-service tokens, and decodes it when it is. A credential
// that is not — a bare OAuth 2.0 access token, or anything else — is left for
// the access-token path to judge.
func arDecodeRegistryToken(credential string) (arRegistryTokenEnvelope, bool) {
	raw, err := base64.StdEncoding.DecodeString(credential)
	if err != nil {
		return arRegistryTokenEnvelope{}, false
	}
	var envelope arRegistryTokenEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return arRegistryTokenEnvelope{}, false
	}
	return envelope, envelope.Product == arRegistryTokenProduct
}

// arTokenServiceHandler serves the Docker token service at the realm every
// Artifact Registry challenge names. A Docker client reaches it with the HTTP
// Basic credential from its credential store, and presents the token it returns
// as the Bearer of the retried request.
//
// A scope the caller cannot reach is refused at the mint. The live service
// answers an uncredentialled mint for a repository scope with the same denial
// the data plane gives, naming the IAM permission and the resource:
//
//	$ curl 'https://europe-west1-docker.pkg.dev/v2/token?service=europe-west1-docker.pkg.dev&scope=repository:no-such-project/no-such-repo/app:pull'
//	HTTP/2 403
//	{"errors":[{"code":"DENIED","message":"Unauthenticated request. Unauthenticated requests do not have permission \"artifactregistry.repositories.downloadArtifacts\" on resource \"projects/no-such-project/locations/europe-west1/repositories/no-such-repo\" (or it may not exist)"}]}
//
// and the same shape for `:push` and `:pull,push`, naming
// `artifactregistry.repositories.uploadArtifacts`. A scope that names no
// repository is minted anonymously as before — `registry:catalog:*` and a
// request carrying no scope at all both return a token — which is what lets a
// client reach the base endpoint and discover the challenge.
//
// What is still not a gate is the scope of a token once issued: one minted for
// `repository:cloudrun/container/hello:pull` serves a request against
// `cloudrun/container/job` too, so the data plane re-evaluates access per
// request against the repository addressed. Both hold at once — the mint
// refuses what the caller plainly cannot reach, and the token it does issue
// carries an identity rather than a permission.
//
// The routes are mounted in artifactregistry.go beside the /v2/ subtree the
// challenge protects.
func arTokenServiceHandler(w http.ResponseWriter, r *http.Request) {
	envelope := arRegistryTokenEnvelope{TokenID: arTokenID(), Product: arRegistryTokenProduct}
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		if repo, permission, scoped := arScopedRepository(r); scoped {
			if resource, located := arRepositoryResource(r, repo); located {
				arPermissionDenied(w, arPrincipal{anonymous: true}, permission, resource)
				return
			}
			// The scope named a repository at a coordinate that names no
			// location, and this registry holds none by that name, so there is
			// no Artifact Registry resource to report a verdict about. A
			// location is not invented to name one, exactly as the data plane
			// does not: the token is issued, and the data plane answers
			// NAME_UNKNOWN when the client presents it.
		}
	}
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" {
		principal, _, err := arAuthenticate(r)
		if err != nil {
			// The token service reports a credential it could not accept with
			// its own message, distinct from the data plane's:
			//
			//	$ curl -i -u 'oauth2accesstoken:bogus' \
			//	    'https://us-central1-docker.pkg.dev/v2/token?service=…&scope=…'
			//	HTTP/2 401
			//	{"errors":[{"code":"UNAUTHORIZED","message":"authentication failed"}]}
			arRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication failed")
			return
		}
		// The token carries the identity forward as an access token the data
		// plane verifies when the client presents it back as a Bearer.
		now := time.Now()
		envelope.AccessToken = signAccessToken(principal.subject, now, now.Add(arRegistryTokenTTL))
	}
	token, err := json.Marshal(envelope)
	if err != nil {
		sim.GCPError(w, http.StatusInternalServerError, "encode registry token", "INTERNAL")
		return
	}
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"token":      base64.StdEncoding.EncodeToString(token),
		"expires_in": int(arRegistryTokenTTL.Seconds()),
	})
}

// arScopedRepository reads the repository a token request asks for out of its
// `scope` parameter, with the IAM permission the actions it asks for require.
//
// The scope grammar is the Docker Registry v2 one,
// `repository:<name>:<action>[,<action>…]`, and Artifact Registry maps it onto
// two permissions: anything that includes `push` needs uploadArtifacts, and
// everything else — `pull`, and `delete`, whose mint the live service answers
// with the download permission — needs downloadArtifacts. A scope of any other
// type, `registry:catalog:*` among them, names no repository.
func arScopedRepository(r *http.Request) (string, string, bool) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	rest, ok := strings.CutPrefix(scope, "repository:")
	if !ok {
		return "", "", false
	}
	name, actions, ok := strings.Cut(rest, ":")
	if !ok || name == "" {
		return "", "", false
	}
	permission := arPermDownload
	for _, action := range strings.Split(actions, ",") {
		if strings.EqualFold(strings.TrimSpace(action), "push") {
			permission = arPermUpload
			break
		}
	}
	return name, permission, true
}

// arTokenServiceMethodNotAllowed answers every verb the token service does not
// serve. The live endpoint is GET-only:
//
//	$ curl -i -X POST 'https://us-docker.pkg.dev/v2/token?service=…&scope=…'
//	HTTP/2 405
func arTokenServiceMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	w.WriteHeader(http.StatusMethodNotAllowed)
}

// arTokenID draws the `token_id` the token envelope carries — a positive
// 63-bit integer, the shape the live service emits (e.g.
// 4268243893102209432).
func arTokenID() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		// The only failure mode is the platform entropy source, which is not a
		// condition the registry can serve through.
		panic(fmt.Sprintf("draw Artifact Registry token id: %v", err))
	}
	return n.Int64() + 1
}
