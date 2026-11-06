package main

import (
	"crypto"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"github.com/google/uuid"
)

// cognitoCode is an authorization code the authorize or login endpoint issued.
type cognitoCode struct {
	ClientId      string
	Username      string
	RedirectURI   string
	Scopes        []string
	Nonce         string
	CodeChallenge string
	AuthTime      time.Time
	Expires       time.Time
}

// cognitoSession is a managed login session, which the session cookie names.
type cognitoSession struct {
	Username string
	AuthTime time.Time
	Expires  time.Time
}

type cognitoRefreshToken struct {
	ClientId  string
	Username  string
	Scopes    []string
	OriginJTI string
	AuthTime  time.Time
	Expires   time.Time
}

var (
	cognitoCodes         sim.Store[cognitoCode]
	cognitoSessions      sim.Store[cognitoSession]
	cognitoRefreshTokens sim.Store[cognitoRefreshToken]
)

const (
	// "The authorization code is valid for five minutes."
	cognitoCodeLifetime = 5 * time.Minute
	// "Amazon Cognito sets session cookies that are valid for 1 hour."
	cognitoSessionLifetime = time.Hour
	cognitoSessionCookie   = "cognito"
)

func cognitoPrefixDomainSuffix() string { return ".auth." + awsRegion() + ".amazoncognito.com" }

func cognitoIssuerHost() string { return "cognito-idp." + awsRegion() + ".amazonaws.com" }

func cognitoIssuer(poolID string) string { return "https://" + cognitoIssuerHost() + "/" + poolID }

func registerCognitoHostedPlane(srv *sim.Server) {
	cognitoCodes = sim.MakeStore[cognitoCode](srv.DB(), "cognito_authorization_codes")
	cognitoSessions = sim.MakeStore[cognitoSession](srv.DB(), "cognito_login_sessions")
	cognitoRefreshTokens = sim.MakeStore[cognitoRefreshToken](srv.DB(), "cognito_refresh_tokens")
	srv.WrapHandler(lbplane.HostRouter{
		Domains: []string{cognitoPrefixDomainSuffix(), cognitoIssuerHost()},
		Route: func(r *http.Request, hostname string) (http.Handler, bool) {
			if hostname == cognitoIssuerHost() {
				return cognitoIssuerRoute(r)
			}
			domain, ok := cognitoDomains.Get(strings.TrimSuffix(hostname, cognitoPrefixDomainSuffix()))
			if !ok {
				return nil, false
			}
			pool, ok := cognitoPools.Get(domain.UserPoolId)
			if !ok {
				return nil, false
			}
			return cognitoHostedRoute(r, pool)
		},
	}.Wrap)
}

func cognitoHostedRoute(r *http.Request, pool CognitoUserPool) (http.Handler, bool) {
	var serve func(http.ResponseWriter, *http.Request, CognitoUserPool)
	switch {
	case r.URL.Path == "/oauth2/authorize" && r.Method == http.MethodGet:
		serve = cognitoServeAuthorize
	case r.URL.Path == "/login" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		serve = cognitoServeLogin
	case r.URL.Path == "/oauth2/token" && r.Method == http.MethodPost:
		serve = cognitoServeToken
	case r.URL.Path == "/oauth2/userInfo" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		serve = cognitoServeUserInfo
	case r.URL.Path == "/logout" && r.Method == http.MethodGet:
		serve = cognitoServeLogout
	default:
		return nil, false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serve(w, r, pool) }), true
}

// The issuer, https://cognito-idp.<region>.amazonaws.com/<pool id>, publishes
// the pool's OpenID configuration and signing keys.
func cognitoIssuerRoute(r *http.Request) (http.Handler, bool) {
	if r.Method != http.MethodGet {
		return nil, false
	}
	poolID, document, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !ok || (document != ".well-known/jwks.json" && document != ".well-known/openid-configuration") {
		return nil, false
	}
	pool, ok := cognitoPools.Get(poolID)
	if !ok {
		return nil, false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if document == ".well-known/jwks.json" {
			cognitoServeJWKS(w, pool)
			return
		}
		cognitoServeOpenIDConfiguration(w, pool)
	}), true
}

func cognitoServeJWKS(w http.ResponseWriter, pool CognitoUserPool) {
	key, err := cognitoSigningKey(pool)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"alg": "RS256",
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		"kid": pool.KeyID,
		"kty": "RSA",
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"use": "sig",
	}}})
}

func cognitoServeOpenIDConfiguration(w http.ResponseWriter, pool CognitoUserPool) {
	config := map[string]any{
		"issuer":                                cognitoIssuer(pool.Id),
		"jwks_uri":                              cognitoIssuer(pool.Id) + "/.well-known/jwks.json",
		"response_types_supported":              []string{"code", "token"},
		"scopes_supported":                      []string{"openid", "email", "phone", "profile"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
	}
	if pool.Domain != "" {
		base := "https://" + cognitoPrefixDomainHost(pool.Domain)
		config["authorization_endpoint"] = base + "/oauth2/authorize"
		config["token_endpoint"] = base + "/oauth2/token"
		config["userinfo_endpoint"] = base + "/oauth2/userInfo"
		config["end_session_endpoint"] = base + "/logout"
	}
	sim.WriteJSON(w, http.StatusOK, config)
}

func cognitoSigningKey(pool CognitoUserPool) (*rsa.PrivateKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(pool.SigningKey)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("user pool %s holds a %T signing key", pool.Id, parsed)
	}
	return key, nil
}

func cognitoHashPassword(password string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, 210000, 32)
}

func cognitoPasswordMatches(user CognitoUser, password string) bool {
	hash, err := cognitoHashPassword(password, user.PasswordSalt)
	return err == nil && subtle.ConstantTimeCompare(hash, user.PasswordHash) == 1
}

// cognitoErrorPage answers a request the endpoint cannot redirect: the client
// or the redirect URI it names is not one the pool knows.
func cognitoErrorPage(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html;charset=UTF-8")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, "<!DOCTYPE html><html><head><title>Error</title></head><body><p>%s</p></body></html>", html.EscapeString(message))
}

// cognitoAuthRequest is an authorization request as the authorize, login and
// logout endpoints receive it.
type cognitoAuthRequest struct {
	client       CognitoUserPoolClient
	redirectURI  string
	responseType string
	state        string
	nonce        string
	challenge    string
	scopes       []string
	prompt       string
}

// cognitoReadAuthRequest checks the client and redirect URI, which an error
// page answers, then the rest, which a redirect to the redirect URI answers.
func cognitoReadAuthRequest(w http.ResponseWriter, query url.Values, pool CognitoUserPool) (cognitoAuthRequest, bool) {
	client, ok := cognitoClients.Get(pool.Id + "/" + query.Get("client_id"))
	if !ok || query.Get("client_id") == "" {
		cognitoErrorPage(w, "An error was encountered with the requested page: client_id is not valid.")
		return cognitoAuthRequest{}, false
	}
	req := cognitoAuthRequest{
		client:       client,
		redirectURI:  query.Get("redirect_uri"),
		responseType: query.Get("response_type"),
		state:        query.Get("state"),
		nonce:        query.Get("nonce"),
		challenge:    query.Get("code_challenge"),
		prompt:       query.Get("prompt"),
	}
	if !slices.Contains(client.CallbackURLs, req.redirectURI) {
		cognitoErrorPage(w, "An error was encountered with the requested page: redirect_mismatch.")
		return req, false
	}
	switch {
	case req.responseType != "code" && req.responseType != "token":
		cognitoRedirectError(w, req, "invalid_request")
		return req, false
	case req.challenge != "" && query.Get("code_challenge_method") != "S256",
		req.challenge == "" && query.Get("code_challenge_method") != "":
		cognitoRedirectError(w, req, "invalid_request")
		return req, false
	case !client.AllowedOAuthFlowsUserPoolClient ||
		!slices.Contains(client.AllowedOAuthFlows, map[string]string{"code": "code", "token": "implicit"}[req.responseType]) ||
		!slices.Contains(client.SupportedIdentityProviders, "COGNITO"):
		cognitoRedirectError(w, req, "unauthorized_client")
		return req, false
	}
	if idp := query.Get("identity_provider"); idp != "" && idp != "COGNITO" {
		cognitoRedirectError(w, req, "unauthorized_client")
		return req, false
	}
	requested := strings.Fields(query.Get("scope"))
	if len(requested) == 0 {
		requested = client.AllowedOAuthScopes
	}
	for _, scope := range requested {
		if !cognitoScopePattern.MatchString(scope) {
			cognitoRedirectError(w, req, "invalid_scope")
			return req, false
		}
		// "Any scope used must be associated with a client, or it will be
		// ignored at runtime."
		if slices.Contains(client.AllowedOAuthScopes, scope) {
			req.scopes = append(req.scopes, scope)
		}
	}
	if !slices.Contains(req.scopes, "openid") && slices.ContainsFunc(req.scopes, func(s string) bool {
		return s == "phone" || s == "email" || s == "profile"
	}) {
		cognitoRedirectError(w, req, "invalid_scope")
		return req, false
	}
	return req, true
}

func cognitoRedirectError(w http.ResponseWriter, req cognitoAuthRequest, code string) {
	values := url.Values{"error": {code}}
	if req.state != "" {
		values.Set("state", req.state)
	}
	w.Header().Set("Location", cognitoAppendQuery(req.redirectURI, values))
	w.WriteHeader(http.StatusFound)
}

func cognitoAppendQuery(raw string, values url.Values) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := u.Query()
	for k, v := range values {
		query[k] = v
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func cognitoServeAuthorize(w http.ResponseWriter, r *http.Request, pool CognitoUserPool) {
	req, ok := cognitoReadAuthRequest(w, r.URL.Query(), pool)
	if !ok {
		return
	}
	session, signedIn := cognitoCurrentSession(r, pool)
	switch {
	case req.prompt == "none" && !signedIn:
		cognitoRedirectError(w, req, "login_required")
	case signedIn && req.prompt != "login":
		cognitoGrant(w, r, pool, req, session)
	default:
		w.Header().Set("Location", "/login?"+r.URL.RawQuery)
		w.WriteHeader(http.StatusFound)
	}
}

func cognitoCurrentSession(r *http.Request, pool CognitoUserPool) (cognitoSession, bool) {
	cookie, err := r.Cookie(cognitoSessionCookie)
	if err != nil {
		return cognitoSession{}, false
	}
	session, ok := cognitoSessions.Get(pool.Id + "/" + cookie.Value)
	if !ok || time.Now().After(session.Expires) {
		return cognitoSession{}, false
	}
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, session.Username))
	if !ok || !user.Enabled {
		return cognitoSession{}, false
	}
	return session, true
}

// cognitoGrant answers an authorization request for a signed-in user: a code
// in the redirect URI's query, or the tokens of an implicit grant in its
// fragment.
func cognitoGrant(w http.ResponseWriter, r *http.Request, pool CognitoUserPool, req cognitoAuthRequest, session cognitoSession) {
	if req.responseType == "code" {
		code := uuid.NewString()
		cognitoCodes.Prune(func(c cognitoCode) bool { return time.Now().After(c.Expires) })
		cognitoCodes.Put(pool.Id+"/"+code, cognitoCode{
			ClientId: req.client.ClientId, Username: session.Username, RedirectURI: req.redirectURI,
			Scopes: req.scopes, Nonce: req.nonce, CodeChallenge: req.challenge, AuthTime: session.AuthTime,
			Expires: time.Now().Add(cognitoCodeLifetime),
		})
		values := url.Values{"code": {code}}
		if req.state != "" {
			values.Set("state", req.state)
		}
		w.Header().Set("Location", cognitoAppendQuery(req.redirectURI, values))
		w.WriteHeader(http.StatusFound)
		return
	}
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, session.Username))
	if !ok {
		cognitoRedirectError(w, req, "access_denied")
		return
	}
	tokens, err := cognitoIssueTokens(pool, req.client, user, req.scopes, req.nonce, session.AuthTime, uuid.NewString())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fragment := url.Values{"access_token": {tokens.access}, "token_type": {"Bearer"}, "expires_in": {fmt.Sprint(tokens.expiresIn)}}
	if tokens.id != "" {
		fragment.Set("id_token", tokens.id)
	}
	if req.state != "" {
		fragment.Set("state", req.state)
	}
	w.Header().Set("Location", req.redirectURI+"#"+fragment.Encode())
	w.WriteHeader(http.StatusFound)
}

func cognitoServeLogin(w http.ResponseWriter, r *http.Request, pool CognitoUserPool) {
	req, ok := cognitoReadAuthRequest(w, r.URL.Query(), pool)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		cognitoLoginPage(w, r, "")
		return
	}
	if err := r.ParseForm(); err != nil {
		cognitoLoginPage(w, r, "Incorrect username or password.")
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, username))
	if !ok || !cognitoPasswordMatches(user, password) {
		cognitoLoginPage(w, r, "Incorrect username or password.")
		return
	}
	switch {
	case !user.Enabled:
		cognitoLoginPage(w, r, "User is disabled.")
		return
	case user.Status == "FORCE_CHANGE_PASSWORD" && time.Now().After(user.TemporaryExpires):
		cognitoLoginPage(w, r, "Temporary password has expired and must be reset by an administrator.")
		return
	case user.Status != "CONFIRMED":
		cognitoLoginPage(w, r, "This user must change their password before they can sign in.")
		return
	}
	now := time.Now()
	id := cognitoRandom(cognitoAlnum, 43)
	session := cognitoSession{Username: user.Username, AuthTime: now, Expires: now.Add(cognitoSessionLifetime)}
	cognitoSessions.Prune(func(s cognitoSession) bool { return now.After(s.Expires) })
	cognitoSessions.Put(pool.Id+"/"+id, session)
	http.SetCookie(w, &http.Cookie{
		Name: cognitoSessionCookie, Value: id, Path: "/", Expires: session.Expires,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	cognitoGrant(w, r, pool, req, session)
}

func cognitoLoginPage(w http.ResponseWriter, r *http.Request, message string) {
	w.Header().Set("Content-Type", "text/html;charset=UTF-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
	var errorLine string
	if message != "" {
		errorLine = `<p id="loginErrorMessage">` + html.EscapeString(message) + `</p>`
	}
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Signin</title></head><body>%s
<form name="cognitoSignInForm" method="post" action="/login?%s">
<label for="signInFormUsername">Username</label><input id="signInFormUsername" name="username" type="text" required>
<label for="signInFormPassword">Password</label><input id="signInFormPassword" name="password" type="password" required>
<input name="signInSubmitButton" type="submit" value="Sign in">
</form></body></html>`, errorLine, html.EscapeString(r.URL.RawQuery))
}

type cognitoTokens struct {
	id, access, refresh string
	expiresIn           int
}

func cognitoIssueTokens(pool CognitoUserPool, client CognitoUserPoolClient, user CognitoUser, scopes []string, nonce string, authTime time.Time, originJTI string) (cognitoTokens, error) {
	key, err := cognitoSigningKey(pool)
	if err != nil {
		return cognitoTokens{}, err
	}
	now := time.Now()
	sub, _ := cognitoUserAttributes(user.Attributes, "sub")
	accessLife := cognitoValidity(client.AccessTokenValidity, client.TokenValidityUnits.AccessToken)
	access := map[string]any{
		"sub": sub, "iss": cognitoIssuer(pool.Id), "version": 2, "client_id": client.ClientId,
		"origin_jti": originJTI, "event_id": uuid.NewString(), "token_use": "access",
		"scope": strings.Join(scopes, " "), "auth_time": authTime.Unix(), "iat": now.Unix(),
		"exp": now.Add(accessLife).Unix(), "jti": uuid.NewString(), "username": user.Username,
	}
	tokens := cognitoTokens{expiresIn: int(accessLife.Seconds())}
	if tokens.access, err = cognitoSign(key, pool.KeyID, access); err != nil {
		return tokens, err
	}
	if !slices.Contains(scopes, "openid") {
		return tokens, nil
	}
	id := map[string]any{}
	for name, value := range cognitoReadableClaims(pool, client, user, scopes) {
		if name == "email_verified" || name == "phone_number_verified" {
			id[name] = value == "true"
			continue
		}
		id[name] = value
	}
	for k, v := range map[string]any{
		"sub": sub, "aud": client.ClientId, "iss": cognitoIssuer(pool.Id), "cognito:username": user.Username,
		"origin_jti": originJTI, "event_id": access["event_id"], "token_use": "id", "auth_time": authTime.Unix(),
		"iat": now.Unix(), "exp": now.Add(cognitoValidity(client.IdTokenValidity, client.TokenValidityUnits.IdToken)).Unix(),
		"jti": uuid.NewString(),
	} {
		id[k] = v
	}
	if nonce != "" {
		id["nonce"] = nonce
	}
	tokens.id, err = cognitoSign(key, pool.KeyID, id)
	return tokens, err
}

// An access or ID token is valid for one hour unless the app client says
// otherwise, in hours unless it names another unit.
func cognitoValidity(value int, unit string) time.Duration {
	if value == 0 {
		return time.Hour
	}
	return time.Duration(cognitoSeconds(value, unit, "hours")) * time.Second
}

// The attributes the scopes of the userInfo endpoint return: openid alone
// returns every attribute the app client can read; profile, email and phone
// return their own sets.
var cognitoScopeAttributes = map[string][]string{
	"profile": {"name", "family_name", "given_name", "middle_name", "nickname", "preferred_username", "profile",
		"picture", "website", "gender", "birthdate", "zoneinfo", "locale", "updated_at"},
	"email": {"email", "email_verified"},
	"phone": {"phone_number", "phone_number_verified"},
}

// cognitoReadableClaims returns the user's attributes the scopes release and
// the app client may read. Without ReadAttributes a client reads the standard
// attributes, email_verified and phone_number_verified.
func cognitoReadableClaims(pool CognitoUserPool, client CognitoUserPoolClient, user CognitoUser, scopes []string) map[string]string {
	readable := func(name string) bool {
		if len(client.ReadAttributes) > 0 {
			return slices.Contains(client.ReadAttributes, name)
		}
		return cognitoStandardAttributes[name]
	}
	var released func(string) bool
	if !slices.ContainsFunc(scopes, func(s string) bool { _, ok := cognitoScopeAttributes[s]; return ok }) {
		released = func(string) bool { return true }
	} else {
		released = func(name string) bool {
			if strings.HasPrefix(name, "custom:") {
				return slices.Contains(scopes, "profile")
			}
			for _, scope := range scopes {
				if slices.Contains(cognitoScopeAttributes[scope], name) {
					return true
				}
			}
			return false
		}
	}
	out := map[string]string{}
	for _, a := range user.Attributes {
		if a.Name != "sub" && readable(a.Name) && released(a.Name) {
			out[a.Name] = a.Value
		}
	}
	return out
}

func cognitoSign(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"kid": kid, "alg": "RS256"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// cognitoVerify checks a token's RS256 signature against the pool's key and
// returns its claims.
func cognitoVerify(pool CognitoUserPool, token string) (map[string]any, error) {
	key, err := cognitoSigningKey(pool)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		return nil, err
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	if exp, ok := claims["exp"].(float64); !ok || time.Now().Unix() >= int64(exp) {
		return nil, errors.New("token expired")
	}
	return claims, nil
}

func cognitoTokenError(w http.ResponseWriter, code, description string) {
	body := map[string]string{"error": code}
	if description != "" {
		body["error_description"] = description
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	sim.WriteJSON(w, http.StatusBadRequest, body)
}

func cognitoServeToken(w http.ResponseWriter, r *http.Request, pool CognitoUserPool) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/x-www-form-urlencoded" {
		cognitoTokenError(w, "invalid_request", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		cognitoTokenError(w, "invalid_request", "")
		return
	}
	form := r.PostForm
	clientID, secret, basic := r.BasicAuth()
	if basic {
		clientID, _ = url.QueryUnescape(clientID)
		secret, _ = url.QueryUnescape(secret)
	} else {
		clientID, secret = form.Get("client_id"), form.Get("client_secret")
	}
	client, ok := cognitoClients.Get(pool.Id + "/" + clientID)
	if !ok || clientID == "" ||
		(client.ClientSecret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(client.ClientSecret)) != 1) ||
		(client.ClientSecret == "" && secret != "") {
		cognitoTokenError(w, "invalid_client", "")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		cognitoRedeemCode(w, form, pool, client)
	case "refresh_token":
		cognitoRedeemRefresh(w, form, pool, client)
	case "client_credentials":
		cognitoClientCredentials(w, form, pool, client)
	case "":
		cognitoTokenError(w, "invalid_request", "")
	default:
		cognitoTokenError(w, "unsupported_grant_type", "")
	}
}

func cognitoRedeemCode(w http.ResponseWriter, form url.Values, pool CognitoUserPool, client CognitoUserPoolClient) {
	if form.Get("code") == "" || form.Get("redirect_uri") == "" {
		cognitoTokenError(w, "invalid_request", "")
		return
	}
	key := pool.Id + "/" + form.Get("code")
	code, ok := cognitoCodes.Get(key)
	if !ok || code.ClientId != client.ClientId || time.Now().After(code.Expires) {
		cognitoTokenError(w, "invalid_grant", "")
		return
	}
	if code.RedirectURI != form.Get("redirect_uri") {
		cognitoTokenError(w, "unauthorized_client", "invalid_redirect")
		return
	}
	if code.CodeChallenge != "" {
		digest := sha256.Sum256([]byte(form.Get("code_verifier")))
		if form.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(digest[:]) != code.CodeChallenge {
			cognitoTokenError(w, "invalid_grant", "")
			return
		}
	}
	// "Authorization codes are single use."
	cognitoCodes.Delete(key)
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, code.Username))
	if !ok || !user.Enabled {
		cognitoTokenError(w, "invalid_grant", "")
		return
	}
	originJTI := uuid.NewString()
	tokens, err := cognitoIssueTokens(pool, client, user, code.Scopes, code.Nonce, code.AuthTime, originJTI)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	refresh := cognitoRandom(cognitoAlnum, 64)
	cognitoRefreshTokens.Prune(func(t cognitoRefreshToken) bool { return time.Now().After(t.Expires) })
	cognitoRefreshTokens.Put(pool.Id+"/"+refresh, cognitoRefreshToken{
		ClientId: client.ClientId, Username: user.Username, Scopes: code.Scopes, OriginJTI: originJTI,
		AuthTime: code.AuthTime, Expires: time.Now().Add(time.Duration(cognitoSeconds(client.RefreshTokenValidity, client.TokenValidityUnits.RefreshToken, "days")) * time.Second),
	})
	tokens.refresh = refresh
	cognitoWriteTokens(w, tokens)
}

func cognitoWriteTokens(w http.ResponseWriter, tokens cognitoTokens) {
	body := map[string]any{"access_token": tokens.access, "token_type": "Bearer", "expires_in": tokens.expiresIn}
	if tokens.id != "" {
		body["id_token"] = tokens.id
	}
	if tokens.refresh != "" {
		body["refresh_token"] = tokens.refresh
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	sim.WriteJSON(w, http.StatusOK, body)
}

func cognitoRedeemRefresh(w http.ResponseWriter, form url.Values, pool CognitoUserPool, client CognitoUserPoolClient) {
	if form.Get("refresh_token") == "" {
		cognitoTokenError(w, "invalid_request", "")
		return
	}
	refresh, ok := cognitoRefreshTokens.Get(pool.Id + "/" + form.Get("refresh_token"))
	if !ok || refresh.ClientId != client.ClientId || time.Now().After(refresh.Expires) {
		cognitoTokenError(w, "invalid_grant", "")
		return
	}
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, refresh.Username))
	if !ok || !user.Enabled {
		cognitoTokenError(w, "invalid_grant", "")
		return
	}
	tokens, err := cognitoIssueTokens(pool, client, user, refresh.Scopes, "", refresh.AuthTime, refresh.OriginJTI)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cognitoWriteTokens(w, tokens)
}

// A client-credentials grant authorizes the app client itself: its access
// token names the client as its subject and carries no user claims.
func cognitoClientCredentials(w http.ResponseWriter, form url.Values, pool CognitoUserPool, client CognitoUserPoolClient) {
	if client.ClientSecret == "" || !slices.Contains(client.AllowedOAuthFlows, "client_credentials") {
		cognitoTokenError(w, "unauthorized_client", "")
		return
	}
	scopes := strings.Fields(form.Get("scope"))
	if len(scopes) == 0 {
		scopes = client.AllowedOAuthScopes
	}
	for _, scope := range scopes {
		if !slices.Contains(client.AllowedOAuthScopes, scope) {
			cognitoTokenError(w, "invalid_grant", "")
			return
		}
	}
	key, err := cognitoSigningKey(pool)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	life := cognitoValidity(client.AccessTokenValidity, client.TokenValidityUnits.AccessToken)
	access, err := cognitoSign(key, pool.KeyID, map[string]any{
		"sub": client.ClientId, "iss": cognitoIssuer(pool.Id), "version": 2, "client_id": client.ClientId,
		"event_id": uuid.NewString(), "token_use": "access", "scope": strings.Join(scopes, " "),
		"auth_time": now.Unix(), "iat": now.Unix(), "exp": now.Add(life).Unix(), "jti": uuid.NewString(),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cognitoWriteTokens(w, cognitoTokens{access: access, expiresIn: int(life.Seconds())})
}

func cognitoUserInfoError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`error="%s", error_description="%s"`, code, description))
	w.WriteHeader(status)
}

func cognitoServeUserInfo(w http.ResponseWriter, r *http.Request, pool CognitoUserPool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		cognitoUserInfoError(w, http.StatusBadRequest, "invalid_request", "Bad OAuth2 request at UserInfo Endpoint")
		return
	}
	const invalid = "Access token is expired, disabled, or deleted, or the user has globally signed out."
	claims, err := cognitoVerify(pool, token)
	if err != nil || claims["token_use"] != "access" || claims["iss"] != cognitoIssuer(pool.Id) {
		cognitoUserInfoError(w, http.StatusUnauthorized, "invalid_token", invalid)
		return
	}
	username, _ := claims["username"].(string)
	clientID, _ := claims["client_id"].(string)
	scopeClaim, _ := claims["scope"].(string)
	scopes := strings.Fields(scopeClaim)
	user, userOK := cognitoUsers.Get(cognitoUserKey(pool, username))
	client, clientOK := cognitoClients.Get(pool.Id + "/" + clientID)
	if username == "" || !userOK || !user.Enabled || !clientOK || !slices.Contains(scopes, "openid") {
		cognitoUserInfoError(w, http.StatusUnauthorized, "invalid_token", invalid)
		return
	}
	body := map[string]string{"username": user.Username}
	for name, value := range cognitoReadableClaims(pool, client, user, scopes) {
		body[name] = value
	}
	body["sub"], _ = cognitoUserAttributes(user.Attributes, "sub")
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
	sim.WriteJSON(w, http.StatusOK, body)
}

func cognitoServeLogout(w http.ResponseWriter, r *http.Request, pool CognitoUserPool) {
	query := r.URL.Query()
	client, ok := cognitoClients.Get(pool.Id + "/" + query.Get("client_id"))
	if !ok || query.Get("client_id") == "" {
		cognitoErrorPage(w, "An error was encountered with the requested page: client_id is not valid.")
		return
	}
	var destination string
	switch {
	case query.Get("logout_uri") != "":
		if !slices.Contains(client.LogoutURLs, query.Get("logout_uri")) {
			cognitoErrorPage(w, "An error was encountered with the requested page: logout_uri is not an allowed sign-out URL.")
			return
		}
		destination = query.Get("logout_uri")
	case query.Get("redirect_uri") != "":
		req, ok := cognitoReadAuthRequest(w, query, pool)
		if !ok {
			return
		}
		next := url.Values{"client_id": {client.ClientId}, "redirect_uri": {req.redirectURI}, "response_type": {req.responseType},
			"scope": {strings.Join(req.scopes, " ")}}
		for _, name := range []string{"state", "nonce"} {
			if v := query.Get(name); v != "" {
				next.Set(name, v)
			}
		}
		destination = "/login?" + next.Encode()
	default:
		cognitoErrorPage(w, "An error was encountered with the requested page: logout_uri or redirect_uri is required.")
		return
	}
	if cookie, err := r.Cookie(cognitoSessionCookie); err == nil {
		cognitoSessions.Delete(pool.Id + "/" + cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cognitoSessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true})
	w.Header().Set("Location", destination)
	w.WriteHeader(http.StatusFound)
}

// cognitoForget removes the codes, sessions and refresh tokens of a pool that
// match.
func cognitoForget(poolID string, match func(clientID, username string) bool) {
	for _, kv := range cognitoCodes.ListPrefix(poolID + "/") {
		if match(kv.Item.ClientId, kv.Item.Username) {
			cognitoCodes.Delete(kv.ID)
		}
	}
	for _, kv := range cognitoSessions.ListPrefix(poolID + "/") {
		if match("", kv.Item.Username) {
			cognitoSessions.Delete(kv.ID)
		}
	}
	for _, kv := range cognitoRefreshTokens.ListPrefix(poolID + "/") {
		if match(kv.Item.ClientId, kv.Item.Username) {
			cognitoRefreshTokens.Delete(kv.ID)
		}
	}
}

func cognitoForgetPool(poolID string) {
	cognitoForget(poolID, func(string, string) bool { return true })
}

func cognitoForgetClient(poolID, clientID string) {
	cognitoForget(poolID, func(c, _ string) bool { return c == clientID })
}

func cognitoForgetUser(poolID, username string) {
	cognitoForget(poolID, func(_, u string) bool { return u == username })
}
