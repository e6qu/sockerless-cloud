// Package uiauth provides first-party OpenID Connect sessions for simulator
// operator interfaces without changing any simulated cloud API route.
package uiauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	LoginPath              = "/auth/oidc/login"
	CallbackPath           = "/auth/oidc/callback"
	SessionPath            = "/auth/session"
	FederationSubjectPath  = "/auth/federation-subject"
	LogoutPath             = "/auth/logout"
	FrontchannelLogoutPath = "/auth/oidc/frontchannel-logout"
	BackchannelLogoutPath  = "/auth/oidc/backchannel-logout"
	LogoutCompletePath     = "/auth/shauth/logout/complete"
	SignedOutPath          = "/auth/signed-out"
	ValidationPath         = "/auth/validation"
	transactionLifetime    = 10 * time.Minute
	maximumFormBytes       = 1 << 20
	backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"
)

var immutableApplicationRelease = regexp.MustCompile(`^(?:[0-9a-f]{12,64}|sha256:[0-9a-f]{64})$`)

type Config struct {
	Issuer          string
	ClientID        string
	ClientSecret    string
	PublicURL       string
	SessionSecret   string
	CookieName      string
	ApplicationName string
	ReleaseRevision string
	SessionLifetime time.Duration
	InsecureCookies bool
}

func (c Config) Enabled() bool {
	return c.Issuer != "" || c.ClientID != "" || c.ClientSecret != "" || c.PublicURL != "" || c.SessionSecret != ""
}

func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	for name, value := range map[string]string{
		"issuer": c.Issuer, "client ID": c.ClientID, "client secret": c.ClientSecret,
		"public URL": c.PublicURL, "session secret": c.SessionSecret, "cookie name": c.CookieName,
		"application release revision": c.ReleaseRevision,
	} {
		if value == "" {
			return fmt.Errorf("OIDC %s is required when simulator UI authentication is enabled", name)
		}
	}
	if len(c.SessionSecret) < 32 {
		return errors.New("OIDC session secret must contain at least 32 bytes")
	}
	if !immutableApplicationRelease.MatchString(c.ReleaseRevision) {
		return errors.New("application release revision must identify an immutable deployed release")
	}
	for name, raw := range map[string]string{"issuer": c.Issuer, "public URL": c.PublicURL} {
		u, err := url.Parse(raw)
		if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("OIDC %s must be an absolute origin URL", name)
		}
		if (!c.InsecureCookies && u.Scheme != "https") ||
			(u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname()))) {
			return fmt.Errorf("OIDC %s must use HTTPS", name)
		}
		if name == "public URL" && strings.TrimRight(u.Path, "/") != "" {
			return errors.New("OIDC public URL must not contain a path")
		}
	}
	return nil
}

type Auth struct {
	config Config
	store  *sessionStore

	providerMu sync.Mutex
	provider   *oidc.Provider
}

func New(config Config) (*Auth, error) {
	if config.SessionLifetime <= 0 {
		config.SessionLifetime = 8 * time.Hour
	}
	config.PublicURL = strings.TrimRight(config.PublicURL, "/")
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Auth{config: config, store: newSessionStore()}, nil
}

func (a *Auth) Enabled() bool { return a != nil && a.config.Enabled() }

// OperatorAssertion returns the OpenID Connect ID token the signed-in operator
// authenticated with for the session bound to r, together with the identity
// coordinates it was issued under: the issuer that signed it and the audience
// (client ID) it was issued for. The console backend brokers this assertion
// into short-lived cloud credentials the way a real console federates a
// signed-in session; keeping it here means the raw assertion never leaves the
// server. It reports false when no operator session is present.
func (a *Auth) OperatorAssertion(r *http.Request) (assertion, issuer, audience string, ok bool) {
	if !a.Enabled() {
		return "", "", "", false
	}
	session, present := a.requestSession(r, time.Now())
	if !present {
		return "", "", "", false
	}
	record, stored := a.store.get(session.ID, time.Now())
	if !stored || record.RawIDToken == "" {
		return "", "", "", false
	}
	return record.RawIDToken, a.config.Issuer, a.config.ClientID, true
}

func (a *Auth) Register(mux *http.ServeMux) {
	if !a.Enabled() {
		return
	}
	mux.HandleFunc("GET "+LoginPath, a.login)
	mux.HandleFunc("GET "+CallbackPath, a.callback)
	mux.HandleFunc("GET "+SessionPath, a.session)
	mux.HandleFunc("GET "+FederationSubjectPath, a.federationSubject)
	mux.HandleFunc("POST "+LogoutPath, a.logout)
	mux.HandleFunc("GET "+FrontchannelLogoutPath, a.frontchannelLogout)
	mux.HandleFunc("POST "+BackchannelLogoutPath, a.backchannelLogout)
	mux.HandleFunc("GET "+LogoutCompletePath, a.logoutComplete)
	mux.HandleFunc("GET "+SignedOutPath, a.signedOut)
	mux.HandleFunc("GET "+ValidationPath, a.validation)
}

func (a *Auth) Protect(next http.Handler) http.Handler {
	if !a.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.requestSession(r, time.Now()); ok {
			next.ServeHTTP(w, r)
			return
		}
		target := r.URL.RequestURI()
		if !strings.HasPrefix(target, "/ui/") {
			target = "/ui/"
		}
		http.Redirect(w, r, LoginPath+"?return_to="+url.QueryEscape(target), http.StatusFound)
	})
}

// oidcDiscoveryClient bounds the issuer discovery and key-set fetches.
// go-oidc otherwise uses http.DefaultClient, whose zero timeout would hold
// providerMu — and with it every login, logout, and back-channel logout —
// for as long as a slow or unreachable issuer keeps the connection open.
var oidcDiscoveryClient = &http.Client{Timeout: 10 * time.Second}

func (a *Auth) providerFor() (*oidc.Provider, error) {
	a.providerMu.Lock()
	defer a.providerMu.Unlock()
	if a.provider != nil {
		return a.provider, nil
	}
	// The provider is cached for the process lifetime and refetches its key
	// set through the context captured here, so discovery runs on a
	// background context with the bounded client rather than the first
	// caller's request context.
	provider, err := oidc.NewProvider(oidc.ClientContext(context.Background(), oidcDiscoveryClient), a.config.Issuer)
	if err != nil {
		return nil, err
	}
	a.provider = provider
	return provider, nil
}

func (a *Auth) oauthConfig(provider *oidc.Provider) oauth2.Config {
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	return oauth2.Config{
		ClientID: a.config.ClientID, ClientSecret: a.config.ClientSecret,
		Endpoint: endpoint, RedirectURL: a.config.PublicURL + CallbackPath,
		Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

type transaction struct {
	State, Nonce, Verifier, ReturnTo string
	Expires                          int64
}

type browserSession struct {
	ID, Subject, Name, Email, Picture, Role string
	Expires                                 int64
}

type sessionRecord struct {
	Subject, UpstreamSID, RawIDToken string
	Expires                          int64
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	provider, err := a.providerFor()
	if err != nil {
		http.Error(w, "OIDC discovery failed", http.StatusBadGateway)
		return
	}
	state, err := randomValue()
	if err != nil {
		http.Error(w, "could not create OIDC transaction", http.StatusInternalServerError)
		return
	}
	nonce, err := randomValue()
	if err != nil {
		http.Error(w, "could not create OIDC transaction", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
	value, err := a.sign(transaction{State: state, Nonce: nonce, Verifier: verifier, ReturnTo: returnTo, Expires: time.Now().Add(transactionLifetime).Unix()})
	if err != nil {
		http.Error(w, "could not create OIDC transaction", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.cookie(a.transactionCookieName(), value, int(transactionLifetime.Seconds())))
	oauthConfig := a.oauthConfig(provider)
	http.Redirect(w, r, oauthConfig.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(a.transactionCookieName())
	a.clearCookie(w, a.transactionCookieName())
	var tx transaction
	if err == nil {
		err = a.verify(cookie.Value, &tx)
	}
	if err != nil || tx.Expires <= time.Now().Unix() || tx.State == "" || r.URL.Query().Get("state") != tx.State || r.URL.Query().Get("code") == "" {
		http.Error(w, "invalid OIDC authorization transaction", http.StatusBadRequest)
		return
	}
	provider, err := a.providerFor()
	if err != nil {
		http.Error(w, "OIDC discovery failed", http.StatusBadGateway)
		return
	}
	oauthConfig := a.oauthConfig(provider)
	tokens, err := oauthConfig.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(tx.Verifier))
	if err != nil {
		http.Error(w, "OIDC token exchange failed", http.StatusBadGateway)
		return
	}
	rawIDToken, ok := tokens.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		http.Error(w, "OIDC response did not contain an ID token", http.StatusBadGateway)
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: a.config.ClientID}).Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "OIDC ID token validation failed", http.StatusForbidden)
		return
	}
	if err := idToken.VerifyAccessToken(tokens.AccessToken); err != nil {
		http.Error(w, "OIDC access token validation failed", http.StatusForbidden)
		return
	}
	var claims struct {
		Nonce             string `json:"nonce"`
		SID               string `json:"sid"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		Picture           string `json:"picture"`
		Role              string `json:"role"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Nonce != tx.Nonce {
		http.Error(w, "OIDC identity validation failed", http.StatusForbidden)
		return
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	if name == "" {
		name = idToken.Subject
	}
	sessionID, err := randomValue()
	if err != nil {
		http.Error(w, "could not create OIDC session", http.StatusInternalServerError)
		return
	}
	expiresAt := time.Now().Add(a.config.SessionLifetime)
	if idToken.Expiry.Before(expiresAt) {
		expiresAt = idToken.Expiry
	}
	session := browserSession{ID: sessionID, Subject: idToken.Subject, Name: name, Email: claims.Email, Picture: claims.Picture, Role: claims.Role, Expires: expiresAt.Unix()}
	value, err := a.sign(session)
	if err != nil {
		http.Error(w, "could not create OIDC session", http.StatusInternalServerError)
		return
	}
	a.store.put(sessionID, sessionRecord{Subject: idToken.Subject, UpstreamSID: claims.SID, RawIDToken: rawIDToken, Expires: session.Expires})
	http.SetCookie(w, a.cookie(a.config.CookieName, value, int(time.Until(expiresAt).Seconds())))
	http.Redirect(w, r, tx.ReturnTo, http.StatusFound)
}

func (a *Auth) session(w http.ResponseWriter, r *http.Request) {
	session, ok := a.requestSession(r, time.Now())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true, "sub": session.Subject, "name": session.Name,
		"email": session.Email, "picture": session.Picture, "role": session.Role,
	})
}

// federationSubject returns the operator's OpenID Connect assertion for the
// current session so the console can federate it into cloud credentials through
// the cloud's own federation endpoint, the way a Workforce Identity Federation
// client holds its subject token. This belongs to the console's authentication
// layer, not to any cloud API; the console reaches the cloud only afterwards,
// with the credential the federation returns. It is not cached, and it requires
// a signed-in operator.
func (a *Auth) federationSubject(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	assertion, issuer, audience, ok := a.OperatorAssertion(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject_token": assertion,
		"issuer":        issuer,
		"audience":      audience,
	})
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameOrigin(r, a.config.PublicURL) {
		http.Error(w, "cross-origin request denied", http.StatusForbidden)
		return
	}
	var rawIDToken string
	if session, ok := a.requestSession(r, time.Now()); ok {
		if record, exists := a.store.get(session.ID, time.Now()); exists {
			rawIDToken = record.RawIDToken
		}
		a.store.delete(session.ID)
	}
	a.clearCookie(w, a.config.CookieName)
	provider, err := a.providerFor()
	if err != nil {
		http.Error(w, "OIDC discovery failed", http.StatusBadGateway)
		return
	}
	var metadata struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil || metadata.EndSessionEndpoint == "" {
		http.Error(w, "OIDC logout endpoint is unavailable", http.StatusBadGateway)
		return
	}
	logoutURL, err := a.logoutURL(metadata.EndSessionEndpoint, rawIDToken)
	if err != nil {
		http.Error(w, "OIDC logout endpoint is invalid", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, logoutURL.String(), http.StatusFound)
}

func (a *Auth) logoutURL(endpoint, rawIDToken string) (*url.URL, error) {
	logoutURL, err := url.Parse(endpoint)
	if err != nil || !logoutURL.IsAbs() || !sameURLOrigin(logoutURL, a.config.Issuer) ||
		(logoutURL.Scheme != "https" && (!a.config.InsecureCookies || logoutURL.Scheme != "http")) {
		return nil, errors.New("invalid OIDC logout endpoint")
	}
	query := logoutURL.Query()
	query.Set("client_id", a.config.ClientID)
	query.Set("post_logout_redirect_uri", a.config.PublicURL+LogoutCompletePath)
	if rawIDToken != "" {
		query.Set("id_token_hint", rawIDToken)
	}
	logoutURL.RawQuery = query.Encode()
	return logoutURL, nil
}

// logoutComplete ignores request input and returns to Shauth's fixed
// completion endpoint.
func (a *Auth) logoutComplete(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(a.config.Issuer)
	if err != nil || !target.IsAbs() || target.Host == "" || target.User != nil {
		http.Error(w, "OIDC issuer is invalid", http.StatusInternalServerError)
		return
	}
	target.Path = "/oauth/logout/complete"
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (a *Auth) backchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		http.Error(w, "content type must be application/x-www-form-urlencoded", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximumFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid logout request", http.StatusBadRequest)
		return
	}
	raw := r.PostForm.Get("logout_token")
	if raw == "" {
		http.Error(w, "logout_token is required", http.StatusBadRequest)
		return
	}
	provider, err := a.providerFor()
	if err != nil {
		http.Error(w, "OIDC discovery failed", http.StatusBadGateway)
		return
	}
	if err := a.processBackchannelLogout(r.Context(), raw, provider.Verifier(&oidc.Config{ClientID: a.config.ClientID}), time.Now()); err != nil {
		http.Error(w, "invalid logout token", http.StatusBadRequest)
		return
	}
	log.Printf("accepted Shauth back-channel logout for client %q", a.config.ClientID)
	w.WriteHeader(http.StatusOK)
}

func (a *Auth) frontchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors "+urlOrigin(a.config.Issuer)+"; base-uri 'none'; form-action 'none'")
	if r.URL.Query().Get("iss") == a.config.Issuer {
		a.store.revoke("", r.URL.Query().Get("sid"), time.Now())
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<!doctype html><html lang=en><title>Signed out</title><body>Signed out</body></html>"))
}

func (a *Auth) processBackchannelLogout(ctx context.Context, raw string, verifier *oidc.IDTokenVerifier, now time.Time) error {
	logoutToken, err := verifier.VerifyLogout(ctx, raw)
	if err != nil {
		return err
	}
	if logoutToken.IssuedAt.IsZero() {
		return errors.New("logout token is missing the iat claim")
	}
	if logoutToken.Expiry.IsZero() || !logoutToken.Expiry.After(now) {
		return errors.New("logout token is missing a valid exp claim")
	}
	var claims struct {
		Events map[string]json.RawMessage `json:"events"`
	}
	if err := logoutToken.Claims(&claims); err != nil || !validLogoutEvent(claims.Events[backchannelLogoutEvent]) {
		return errors.New("logout token contains an invalid back-channel logout event")
	}
	if !a.store.consumeAndRevoke(logoutToken.TokenID, logoutToken.Expiry, logoutToken.Subject, logoutToken.SessionID, now) {
		return errors.New("logout token was already processed")
	}
	return nil
}

func (a *Auth) signedOut(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	_ = signedOutTemplate.Execute(w, map[string]string{"Name": a.config.ApplicationName, "Login": LoginPath})
}

func (a *Auth) validation(w http.ResponseWriter, r *http.Request) {
	session, ok := a.requestSession(r, time.Now())
	if !ok {
		http.Redirect(w, r, SignedOutPath, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'self' "+urlOrigin(a.config.Issuer))
	_ = validationTemplate.Execute(w, map[string]string{
		"Name": session.Name, "Email": session.Email, "Role": session.Role,
		"Release": a.config.ReleaseRevision, "Application": a.config.ApplicationName,
	})
}

var signedOutTemplate = template.Must(template.New("signed-out").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Signed out · {{.Name}}</title><style>:root{color-scheme:light dark}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f5f7ff;color:#17172b;font:16px system-ui,sans-serif}.card{max-width:34rem;padding:2.5rem;border:1px solid #dfe3f0;border-radius:1.25rem;background:#fff;box-shadow:0 16px 40px #24284a14}a{display:inline-block;margin-top:1rem;padding:.7rem 1rem;border-radius:.6rem;background:#7c3aed;color:#fff;font-weight:700;text-decoration:none}a:focus-visible{outline:3px solid #0891b2;outline-offset:3px}@media(prefers-color-scheme:dark){body{background:#0b1020;color:#f5f7ff}.card{background:#151c30;border-color:#2c3652}}</style></head><body><main class="card" aria-labelledby="signed-out-title"><h1 id="signed-out-title">Signed out of {{.Name}}</h1><p role="status">Your local session and shared Shauth session have ended.</p><a href="{{.Login}}">Sign in with Shauth</a></main></body></html>`))

var validationTemplate = template.Must(template.New("validation").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authentication validation · {{.Application}}</title><style>:root{color-scheme:light dark}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f5f7ff;color:#17172b;font:16px system-ui,sans-serif}.card{width:min(34rem,calc(100% - 2rem));padding:2.5rem;border:1px solid #dfe3f0;border-radius:1.25rem;background:#fff;box-shadow:0 16px 40px #24284a14}dl{display:grid;grid-template-columns:max-content 1fr;gap:.65rem 1rem}dt{font-weight:700}dd{margin:0;overflow-wrap:anywhere}button{padding:.7rem 1rem;border:0;border-radius:.6rem;background:#7c3aed;color:#fff;font:inherit;font-weight:700;cursor:pointer}button:focus-visible{outline:3px solid #0891b2;outline-offset:3px}@media(prefers-color-scheme:dark){body{background:#0b1020;color:#f5f7ff}.card{background:#151c30;border-color:#2c3652}}</style></head><body><main class="card" aria-labelledby="validation-title"><h1 id="validation-title">Signed in to {{.Application}}</h1><dl><dt>Username</dt><dd data-testid="validation-username">{{.Name}}</dd><dt>Email</dt><dd data-testid="validation-email">{{.Email}}</dd><dt>Role</dt><dd data-testid="validation-role">{{.Role}}</dd><dt>Release</dt><dd data-testid="validation-release">{{.Release}}</dd></dl><form method="post" action="/auth/logout"><button type="submit">Sign out</button></form></main></body></html>`))

func (a *Auth) requestSession(r *http.Request, now time.Time) (browserSession, bool) {
	var session browserSession
	cookie, err := r.Cookie(a.config.CookieName)
	if err != nil || a.verify(cookie.Value, &session) != nil || session.ID == "" || session.Expires <= now.Unix() {
		return session, false
	}
	record, ok := a.store.get(session.ID, now)
	return session, ok && record.Subject == session.Subject && record.Expires == session.Expires
}

func (a *Auth) transactionCookieName() string { return a.config.CookieName + "_tx" }

func (a *Auth) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: !a.config.InsecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func (a *Auth) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, a.cookie(name, "", -1))
}

func (a *Auth) sign(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(a.config.SessionSecret))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (a *Auth) verify(value string, target any) error {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return errors.New("invalid signed value")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(a.config.SessionSecret))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errors.New("invalid signed value")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

func randomValue() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeReturnTo(value string) string {
	u, err := url.ParseRequestURI(value)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/ui/") {
		return "/ui/"
	}
	return u.RequestURI()
}

func sameOrigin(r *http.Request, publicURL string) bool {
	want, err := url.Parse(publicURL)
	if err != nil {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		got, err := url.Parse(origin)
		return err == nil && sameParsedOrigin(got, want)
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		got, err := url.Parse(referer)
		return err == nil && sameParsedOrigin(got, want)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin" && r.Host == want.Host
}

func sameURLOrigin(got *url.URL, wantRaw string) bool {
	want, err := url.Parse(wantRaw)
	return err == nil && sameParsedOrigin(got, want)
}

func sameParsedOrigin(got, want *url.URL) bool {
	return got != nil && want != nil && got.IsAbs() && got.User == nil &&
		got.Scheme == want.Scheme && got.Host == want.Host
}

func urlOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "'none'"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validLogoutEvent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var event map[string]json.RawMessage
	return json.Unmarshal(raw, &event) == nil && event != nil && len(event) == 0
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type sessionStore struct {
	mu           sync.Mutex
	sessions     map[string]sessionRecord
	logoutTokens map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]sessionRecord), logoutTokens: make(map[string]time.Time)}
}

func (s *sessionStore) put(id string, record sessionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = record
}

func (s *sessionStore) revoke(subject, upstreamSID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	s.revokeLocked(subject, upstreamSID)
}
func (s *sessionStore) delete(id string) { s.mu.Lock(); defer s.mu.Unlock(); delete(s.sessions, id) }
func (s *sessionStore) get(id string, now time.Time) (sessionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	record, ok := s.sessions[id]
	return record, ok
}
func (s *sessionStore) revokeLocked(subject, sid string) {
	for id, record := range s.sessions {
		if (sid != "" && record.UpstreamSID == sid) || (subject != "" && record.Subject == subject) {
			delete(s.sessions, id)
		}
	}
}
func (s *sessionStore) consumeAndRevoke(id string, expiry time.Time, subject, sid string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	if _, exists := s.logoutTokens[id]; exists {
		return false
	}
	s.logoutTokens[id] = expiry
	s.revokeLocked(subject, sid)
	return true
}
func (s *sessionStore) prune(now time.Time) {
	for id, record := range s.sessions {
		if record.Expires <= now.Unix() {
			delete(s.sessions, id)
		}
	}
	for id, expiry := range s.logoutTokens {
		if !expiry.After(now) {
			delete(s.logoutTokens, id)
		}
	}
}
