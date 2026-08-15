package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Authentication for the Azure Container Registry data plane — the Docker
// Registry HTTP API v2 surface under /v2/ and the ACR convenience API under
// /acr/v1/, both served on a registry's own login server.
//
// A registry refuses an unauthenticated request with the Docker Registry v2
// Bearer challenge, which tells the client where its token service lives:
//
//	Www-Authenticate: Bearer realm="https://contosoregistry.azurecr.io/oauth2/token",service="contosoregistry.azurecr.io",scope="repository:hello-world:pull"
//
// (Azure/acr `docs/Token-BasicAuth.md`, "Token with Basic Auth"), and the body
// is the Docker Registry v2 error envelope, whose UNAUTHORIZED code carries the
// message "authentication required" (the error-code table of the Docker
// Registry HTTP API V2 specification). Which credential failed is carried by
// the challenge's `error` parameter, as a token-authenticating registry
// reports it: absent for a request that presented none, "invalid_token" for
// one the registry could not accept, and "insufficient_scope" for a valid
// token that does not carry the access the request needs — the last one being
// what tells a client which scope to ask its token service for next.
//
// Three credentials reach the data plane, and every one of them is verified:
//
//   - HTTP Basic with the registry's admin username and one of the two admin
//     passwords listCredentials serves — what `docker login <loginServer>` and
//     the console send. It authenticates only while the registry's admin user
//     is enabled, and only against the passwords the credential slots serve
//     *now*, so a regenerateCredential invalidates the old one.
//   - A Bearer ACR access token, minted by this registry's token service for a
//     specific set of scopes. The token authorizes exactly the access records
//     of its `access` claim: a token scoped to one repository does not reach
//     another, and one minted by another registry does not reach this one.
//   - No credential at all, which authenticates only the pull of a registry
//     whose anonymousPullEnabled property is set.
//
// The token service itself lives in acr.go (/oauth2/exchange, /oauth2/token);
// this file holds the credential verification both it and the data plane use.

// acrEntraAudience is the Microsoft Entra audience an ACR data-plane token is
// issued for: azcontainerregistry requests the scope
// "https://containerregistry.azure.net/.default" (its cloud configuration's
// default audience) before exchanging the resulting token at /oauth2/exchange.
const acrEntraAudience = "https://containerregistry.azure.net"

// acrTokenTTL is how long an ACR refresh or access token stays valid.
const acrTokenTTL = 3 * time.Hour

// acrAdminPasswordSlots are the two admin credential slots a registry serves,
// in the order listCredentials returns them.
var acrAdminPasswordSlots = []string{"password", "password2"}

// Docker Registry HTTP API v2 access actions. The action a request needs is
// derived from its method exactly as a registry derives its access records:
// a read pulls, a write pulls and pushes, and a removal deletes.
const (
	acrActionPull         = "pull"
	acrActionPush         = "push"
	acrActionDelete       = "delete"
	acrActionMetadataRead = "metadata_read"
	acrActionAll          = "*"
)

// acrAccess is one access record of an ACR access token's `access` claim: the
// resource type, the resource name, and the actions granted on it. It is the
// claim shape Azure documents for an ACR access token, e.g.
// {"type": "registry", "name": "catalog", "actions": ["*"]}
// (Azure/acr `docs/AAD-OAuth.md`).
type acrAccess struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// acrResource is the access one data-plane request needs. challengeActions is
// what the Bearer challenge asks the client to obtain, which for a write is
// the pull,push pair a registry requires to accept an upload.
type acrResource struct {
	typ              string
	name             string
	action           string
	challengeActions string
	// authenticationOnly marks the Docker Registry v2 base endpoint. A client
	// reaches it to discover both API support and the token service, and it
	// addresses no resource — a registry requires a credential it accepts
	// there but no access record, which is why a token acquired with an empty
	// scope answers the ping and reaches nothing else.
	authenticationOnly bool
}

// acrRegistryCatalogResource is the access a request that addresses the
// registry itself — its repository catalog — needs.
func acrRegistryCatalogResource(action string) acrResource {
	return acrResource{typ: "registry", name: "catalog", action: action, challengeActions: acrActionAll}
}

// acrBaseEndpointResource is what the /v2/ base endpoint needs: a credential,
// and nothing more. The challenge it answers with still names the registry's
// catalog scope, so a client that follows it asks the token service for a
// scope the registry issues.
func acrBaseEndpointResource() acrResource {
	res := acrRegistryCatalogResource(acrActionAll)
	res.authenticationOnly = true
	return res
}

// acrRepositoryResource is the access a request against one repository needs.
func acrRepositoryResource(repo, action, challengeActions string) acrResource {
	return acrResource{typ: "repository", name: repo, action: action, challengeActions: challengeActions}
}

// acrV2Resource maps a Docker Registry v2 request onto the access record it
// needs. The mapping is the registry's own: GET and HEAD pull, POST, PUT and
// PATCH pull and push, DELETE deletes. An empty repository is the /v2/ base
// endpoint, which addresses the registry rather than a repository.
func acrV2Resource(r *http.Request, repo string) acrResource {
	if repo == "" {
		return acrBaseEndpointResource()
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return acrRepositoryResource(repo, acrActionPull, acrActionPull)
	case http.MethodDelete:
		return acrRepositoryResource(repo, acrActionDelete, acrActionDelete)
	default:
		return acrRepositoryResource(repo, acrActionPush, acrActionPull+","+acrActionPush)
	}
}

// scope renders the access the request needs as the Docker Registry v2 token
// scope grammar, "<type>:<name>:<actions>", which is what the challenge asks
// the client to acquire.
func (res acrResource) scope() string {
	actions := res.challengeActions
	if actions == "" {
		actions = res.action
	}
	return res.typ + ":" + res.name + ":" + actions
}

// acrAuthorizeV2 is the OCIRegistry.Authorize hook the ACR data plane mounts:
// it maps the /v2/ route the shared registry parsed onto its access record and
// authenticates the request against the registry the Host addresses.
func acrAuthorizeV2(w http.ResponseWriter, r *http.Request, repo string) bool {
	return acrAuthorize(w, r, acrV2Resource(r, repo))
}

// acrAuthorize authenticates one data-plane request. It writes the registry's
// refusal and reports false when the request does not authorize.
func acrAuthorize(w http.ResponseWriter, r *http.Request, res acrResource) bool {
	reg, ok := acrRegistryForHost(r.Host)
	if !ok {
		acrHostNotARegistry(w, r)
		return false
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	basic := acrSchemeValue(authorization, "Basic")
	bearer := acrSchemeValue(authorization, "Bearer")
	switch {
	case authorization == "":
		// Anonymous. Only a registry with anonymous pull enabled serves one,
		// and only the pull it enables.
		if res.action == acrActionPull && acrAnonymousPullEnabled(reg) {
			return true
		}
		acrChallenge(w, r, reg, res, "", "authentication required")
		return false

	case basic != "":
		username, password, decoded := acrBasicCredential(basic)
		if !decoded {
			acrChallenge(w, r, reg, res, "invalid_token", "authentication required")
			return false
		}
		if _, valid := acrAdminCredentialSlot(reg, username, password); !valid {
			acrChallenge(w, r, reg, res, "invalid_token", "authentication required")
			return false
		}
		// The admin credential is the registry's own owner credential; it
		// carries every action on every repository of that registry.
		return true

	case bearer != "":
		claims, err := acrVerifyAccessToken(bearer, reg)
		if err != nil {
			acrChallenge(w, r, reg, res, "invalid_token", "authentication required")
			return false
		}
		if !res.authenticationOnly && !acrAccessGrants(claims.Access, res) {
			// A token that authenticated but does not carry the access the
			// request needs is answered with the challenge again, naming the
			// scope that is missing: that is how a client learns which scope to
			// ask its token service for next.
			acrChallenge(w, r, reg, res, "insufficient_scope", "authentication required")
			return false
		}
		return true

	default:
		acrChallenge(w, r, reg, res, "invalid_token", "authentication required")
		return false
	}
}

// acrAnonymousPullEnabled reads the registry's anonymousPullEnabled property,
// the Microsoft.ContainerRegistry knob that lets an unauthenticated client
// pull from it.
func acrAnonymousPullEnabled(reg Registry) bool {
	return reg.Properties.AnonymousPullEnabled != nil && *reg.Properties.AnonymousPullEnabled
}

// acrSchemeValue returns the credential of an Authorization header when it
// carries the given scheme, and "" otherwise.
func acrSchemeValue(authorization, scheme string) string {
	if len(authorization) <= len(scheme) || !strings.EqualFold(authorization[:len(scheme)], scheme) {
		return ""
	}
	if authorization[len(scheme)] != ' ' {
		return ""
	}
	return strings.TrimSpace(authorization[len(scheme)+1:])
}

// acrBasicCredential splits the base64 "user:password" of a Basic credential.
func acrBasicCredential(encoded string) (string, string, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}
	username, password, ok := strings.Cut(string(raw), ":")
	if !ok {
		return "", "", false
	}
	return username, password, true
}

// acrAdminCredentialSlot reports which admin password slot a presented
// credential matches. It authenticates only while the registry's admin user is
// enabled — the same condition listCredentials requires before it will serve
// the pair — the username is the registry name (the username ACR's admin
// credential carries), and the password is compared against the material each
// slot serves now, so a slot regenerateCredential has rotated authenticates
// under its new value and no longer under its old one.
func acrAdminCredentialSlot(reg Registry, username, password string) (string, bool) {
	if !reg.Properties.AdminUserEnabled {
		return "", false
	}
	if !strings.EqualFold(username, reg.Name) {
		return "", false
	}
	for _, slot := range acrAdminPasswordSlots {
		current := azureKeyMaterial32(reg.ID, slot)
		if hmac.Equal([]byte(current), []byte(password)) {
			return slot, true
		}
	}
	return "", false
}

// acrCredentialFingerprint binds a token to the admin password it was minted
// from. The claim is opaque to clients — only the registry that issued the
// token reads it — and it is what makes a regenerateCredential invalidate the
// tokens the rotated password had already produced: the fingerprint recomputed
// from the slot's new material no longer matches the one in the token.
func acrCredentialFingerprint(registryID, slot string) string {
	sum := sha256.Sum256([]byte(registryID + "|" + slot + "|" + azureKeyMaterial32(registryID, slot)))
	return hex.EncodeToString(sum[:16])
}

// acrAccessGrants reports whether a token's access records authorize a
// request. A record matches on resource type and name, and grants the action
// when it lists it or lists the "*" wildcard ACR uses for full access.
func acrAccessGrants(granted []acrAccess, res acrResource) bool {
	for _, entry := range granted {
		if entry.Type != res.typ || entry.Name != res.name {
			continue
		}
		for _, action := range entry.Actions {
			if action == res.action || action == acrActionAll {
				return true
			}
		}
	}
	return false
}

// --- registry resolution ---

// acrRegistryForHost resolves the registry a data-plane request addresses from
// the host it was sent to. A registry's data plane lives on its own login
// server — the host the control plane advertises in loginServer and the one a
// client dials — so the Host header names the registry exactly as it does on
// real Azure.
func acrRegistryForHost(host string) (Registry, bool) {
	wanted := acrBareHost(host)
	if wanted == "" {
		return Registry{}, false
	}
	for _, reg := range acrRegistries.List() {
		if strings.EqualFold(acrBareHost(reg.Properties.LoginServer), wanted) {
			return reg, true
		}
	}
	return Registry{}, false
}

// acrBareHost strips the port and any scheme from a host value so a login
// server advertised with a port matches the Host a client sends without one.
func acrBareHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i+1:], "]") {
		host = host[:i]
	}
	return host
}

// acrLoginServer returns the login server the challenge and the token service
// name, which is the registry's advertised loginServer.
func acrLoginServer(reg Registry) string {
	return reg.Properties.LoginServer
}

// --- responses ---

// acrChallenge answers with the Docker Registry v2 Bearer challenge and the
// registry's 401 error body. The header names the realm (this registry's token
// service), the service (its login server) and the scope the client needs,
// which is everything a client needs to acquire a token and retry; error is
// added when a credential was presented and rejected.
func acrChallenge(w http.ResponseWriter, r *http.Request, reg Registry, res acrResource, errCode, message string) {
	service := acrLoginServer(reg)
	realm := azureRequestScheme(r) + "://" + service + "/oauth2/token"
	challenge := fmt.Sprintf("Bearer realm=%q,service=%q,scope=%q", realm, service, res.scope())
	if errCode != "" {
		challenge += fmt.Sprintf(",error=%q", errCode)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	acrRegistryErrors(w, http.StatusUnauthorized, "UNAUTHORIZED", message, res)
}

// acrRegistryErrors writes the Docker Registry v2 error envelope, whose detail
// names the resource and the action the request needed.
func acrRegistryErrors(w http.ResponseWriter, status int, code, message string, res acrResource) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	sim.WriteJSON(w, status, map[string]any{
		"errors": []map[string]any{{
			"code":    code,
			"message": message,
			"detail": []map[string]any{{
				"Type":   res.typ,
				"Name":   res.name,
				"Action": res.action,
			}},
		}},
	})
}

// acrHostNotARegistry answers a data-plane request addressed to a host that is
// not a registry login server. On real Azure such a request never reaches a
// registry — the host does not resolve — so there is no published wire shape
// for it. The collapsed-host simulator, where every login server shares one
// listener, answers with the registry error envelope and NAME_UNKNOWN, the
// Docker Registry v2 code for a name the registry does not know.
func acrHostNotARegistry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	sim.WriteJSON(w, http.StatusNotFound, map[string]any{
		"errors": []map[string]any{{
			"code":    "NAME_UNKNOWN",
			"message": "repository name not known to registry",
			"detail":  map[string]any{"Host": r.Host},
		}},
	})
}

// acrOAuthUnauthorized answers a token-service request whose credential did
// not authenticate, with the registry's error envelope.
func acrOAuthUnauthorized(w http.ResponseWriter, message string) {
	sim.WriteJSON(w, http.StatusUnauthorized, map[string]any{
		"errors": []map[string]any{{
			"code":    "UNAUTHORIZED",
			"message": message,
		}},
	})
}

// --- tokens ---

// acrTokenClaims is the payload of the JWTs this registry's token service
// issues. Both token kinds carry the subject, the grant that produced them and
// the service they were issued for; an access token additionally carries the
// access records it grants, and a refresh token the permissions of the
// identity behind it — the claim shapes Azure documents for the two ACR tokens
// (Azure/acr `docs/AAD-OAuth.md`).
type acrTokenClaims struct {
	Subject     string
	GrantType   string
	Audience    string
	Access      []acrAccess
	Permissions *acrTokenPermissions
	// Credential fingerprints the admin password the token was minted from,
	// and is empty for a token minted from a Microsoft Entra identity.
	Credential string
	Slot       string
}

type acrTokenPermissions struct {
	Actions    []string `json:"actions"`
	NotActions []string `json:"notActions"`
}

// acrMintToken signs one of the registry's JWTs. Both token kinds are real
// JWTs because clients read their expiry out of the payload — the Azure SDK
// for Go decodes the refresh token's `exp` claim to decide when to renew it.
func acrMintToken(claims acrTokenClaims) (string, error) {
	now := time.Now()
	payload := map[string]any{
		"jti":        generateUUID(),
		"sub":        claims.Subject,
		"aud":        claims.Audience,
		"grant_type": claims.GrantType,
		"iat":        now.Unix(),
		"nbf":        now.Unix(),
		"exp":        now.Add(acrTokenTTL).Unix(),
		"version":    "2.0",
	}
	if claims.Access != nil {
		payload["access"] = claims.Access
	}
	if claims.Permissions != nil {
		payload["permissions"] = claims.Permissions
	}
	if claims.Credential != "" {
		payload["credential"] = claims.Credential
		payload["credential_slot"] = claims.Slot
	}
	return mintAzureSimSignedJWT(payload)
}

// acrMintRefreshToken issues the ACR refresh token /oauth2/exchange returns.
// It carries the permissions of the identity that obtained it, which for an
// authenticated exchange is every action on the registry.
func acrMintRefreshToken(reg Registry, subject string) (string, error) {
	return acrMintToken(acrTokenClaims{
		Subject:     subject,
		GrantType:   "refresh_token",
		Audience:    acrLoginServer(reg),
		Permissions: &acrTokenPermissions{Actions: []string{acrActionAll}, NotActions: []string{}},
	})
}

// acrMintAccessToken issues the scoped ACR access token /oauth2/token returns.
func acrMintAccessToken(reg Registry, subject string, access []acrAccess, credential, slot string) (string, error) {
	if access == nil {
		access = []acrAccess{}
	}
	return acrMintToken(acrTokenClaims{
		Subject:    subject,
		GrantType:  "access_token",
		Audience:   acrLoginServer(reg),
		Access:     access,
		Credential: credential,
		Slot:       slot,
	})
}

// acrVerifiedToken is a token this registry issued, after verification.
type acrVerifiedToken struct {
	Subject     string
	GrantType   string
	Access      []acrAccess
	Permissions *acrTokenPermissions
}

// acrVerifyToken verifies one of the registry's own JWTs: the signature and
// expiry the shared verifier checks, the grant that must have produced it, the
// registry it was issued for, and — for a token minted from an admin password
// — that the password it came from has not since been regenerated.
func acrVerifyToken(token string, reg Registry, grantType string) (*acrVerifiedToken, error) {
	claims, err := verifyAzureSimJWT(token)
	if err != nil {
		return nil, err
	}
	if got, _ := claims["grant_type"].(string); got != grantType {
		return nil, fmt.Errorf("token grant_type %q is not %q", got, grantType)
	}
	if !strings.EqualFold(acrBareHost(azureTokenAudience(claims)), acrBareHost(acrLoginServer(reg))) {
		return nil, fmt.Errorf("token was issued for another registry")
	}
	if fingerprint, _ := claims["credential"].(string); fingerprint != "" {
		slot, _ := claims["credential_slot"].(string)
		if !hmac.Equal([]byte(acrCredentialFingerprint(reg.ID, slot)), []byte(fingerprint)) {
			return nil, fmt.Errorf("the credential this token was issued from has been regenerated")
		}
	}
	out := &acrVerifiedToken{GrantType: grantType}
	out.Subject, _ = claims["sub"].(string)
	out.Access = acrAccessFromClaim(claims["access"])
	return out, nil
}

// acrVerifyAccessToken verifies a Bearer credential presented to the data
// plane as an access token this registry issued.
func acrVerifyAccessToken(token string, reg Registry) (*acrVerifiedToken, error) {
	return acrVerifyToken(token, reg, "access_token")
}

// acrVerifyRefreshToken verifies the ACR refresh token presented to
// /oauth2/token by the refresh-token grant.
func acrVerifyRefreshToken(token string, reg Registry) (*acrVerifiedToken, error) {
	return acrVerifyToken(token, reg, "refresh_token")
}

// acrAccessFromClaim reads the `access` claim back into access records.
func acrAccessFromClaim(raw any) []acrAccess {
	entries, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]acrAccess, 0, len(entries))
	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		record := acrAccess{}
		record.Type, _ = fields["type"].(string)
		record.Name, _ = fields["name"].(string)
		for _, action := range acrClaimList(fields["actions"]) {
			if s, ok := action.(string); ok {
				record.Actions = append(record.Actions, s)
			}
		}
		out = append(out, record)
	}
	return out
}

// acrClaimList reads a claim member that is expected to be a JSON array.
func acrClaimList(raw any) []any {
	out, _ := raw.([]any)
	return out
}

// --- scopes ---

// acrParseScopes reads the requested token scopes. The scope grammar is the
// Docker Registry v2 one, "<type>:<name>:<action>[,<action>…]", and a request
// may carry several scopes, either repeated or space separated.
func acrParseScopes(values []string) []acrAccess {
	var out []acrAccess
	for _, value := range values {
		for _, field := range strings.Fields(value) {
			parts := strings.SplitN(field, ":", 3)
			if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
				continue
			}
			record := acrAccess{Type: parts[0], Name: parts[1]}
			for _, action := range strings.Split(parts[2], ",") {
				if action = strings.TrimSpace(action); action != "" {
					record.Actions = append(record.Actions, action)
				}
			}
			if len(record.Actions) > 0 {
				out = append(out, record)
			}
		}
	}
	return out
}

// acrGrantScopes filters the scopes a token request asked for down to the ones
// its credential authorizes, which is what a token service issues: the token
// carries the granted access, never the requested access. An owner credential
// (the admin user, or an authenticated Microsoft Entra identity) is granted
// every action it asks for; an anonymous request is granted only the pull that
// a registry with anonymous pull enabled offers.
func acrGrantScopes(requested []acrAccess, owner bool) []acrAccess {
	granted := make([]acrAccess, 0, len(requested))
	for _, record := range requested {
		if owner {
			granted = append(granted, record)
			continue
		}
		var actions []string
		for _, action := range record.Actions {
			if record.Type == "repository" && (action == acrActionPull || action == acrActionAll) {
				actions = append(actions, acrActionPull)
			}
		}
		if len(actions) > 0 {
			granted = append(granted, acrAccess{Type: record.Type, Name: record.Name, Actions: actions})
		}
	}
	return granted
}
