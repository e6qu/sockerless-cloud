package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"github.com/google/uuid"
)

// An Application Load Balancer runs an authenticate-oidc or
// authenticate-cognito action the way the "Authenticate users using an
// Application Load Balancer" guide describes: a request without a session
// goes to the IdP's authorization endpoint, the IdP returns a code to
// /oauth2/idpresponse, the load balancer redeems it at the token endpoint,
// reads the user's claims from the user info endpoint, and keeps them in an
// encrypted session cookie; targets receive the claims in the x-amzn-oidc-*
// headers.

const (
	elbv2AuthDefaultCookie = "AWSELBAuthSessionCookie"
	elbv2AuthNonceCookie   = "AWSALBAuthNonce"
	// "The default is 604800 seconds (7 days)."
	elbv2AuthDefaultSessionTimeout = 604800
	// "The cookie expiry is an attribute of the cookie, which is set to 7 days."
	elbv2AuthCookieLifetime = 7 * 24 * time.Hour
	// "A client must initiate and complete the authentication process within 15
	// minutes."
	elbv2AuthLoginWindow = 15 * time.Minute
	// "the load balancer shards a cookie that is greater than 4K in size into
	// multiple cookies"; "If the total size of the user claims and access token
	// received from the IdP is greater than 11K bytes in size, the load balancer
	// returns an HTTP 500 error."
	elbv2AuthCookieShard  = 4096
	elbv2AuthClaimsLimit  = 11 * 1024
	elbv2AuthCallbackPath = "/oauth2/idpresponse"
)

// elbv2AuthKey is a key the load balancers of the Region hold: the cookie key
// of one load balancer, or the Region's claims-signing key.
type elbv2AuthKey struct {
	ID  string
	Key []byte
}

var (
	elbv2AuthKeys  sim.Store[elbv2AuthKey]
	elbv2AuthKeyMu sync.Mutex
)

const elbv2SigningKeyID = "claims-signing"

func elbv2PublicKeysHost() string { return "public-keys.auth.elb." + awsRegion() + ".amazonaws.com" }

func registerELBv2Authentication(srv *sim.Server) {
	elbv2AuthKeys = sim.MakeStore[elbv2AuthKey](srv.DB(), "elbv2_auth_keys")
	// "To get the public key, get the key ID from the JWT header and use it to
	// look up the public key from the endpoint":
	// https://public-keys.auth.elb.<region>.amazonaws.com/<key-id>.
	srv.WrapHandler(lbplane.HostRouter{
		Domains: []string{elbv2PublicKeysHost()},
		Route: func(r *http.Request, _ string) (http.Handler, bool) {
			if r.Method != http.MethodGet {
				return nil, false
			}
			return http.HandlerFunc(elbv2ServePublicKey), true
		},
		NotFound: http.NotFoundHandler(),
	}.Wrap)
}

func elbv2ServePublicKey(w http.ResponseWriter, r *http.Request) {
	key, kid, err := elbv2ClaimsSigningKey()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.TrimPrefix(r.URL.Path, "/") != kid {
		http.NotFound(w, r)
		return
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	_ = pem.Encode(w, &pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// elbv2ClaimsSigningKey returns the Region's ES256 key and its key ID, which
// has the form of the guide's example, a UUID.
func elbv2ClaimsSigningKey() (*ecdsa.PrivateKey, string, error) {
	stored, err := elbv2AuthKeyFor(elbv2SigningKeyID, func() (elbv2AuthKey, error) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return elbv2AuthKey{}, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		return elbv2AuthKey{ID: uuid.NewString(), Key: der}, err
	})
	if err != nil {
		return nil, "", err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(stored.Key)
	if err != nil {
		return nil, "", err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, "", fmt.Errorf("claims-signing key is a %T", parsed)
	}
	return key, stored.ID, nil
}

// elbv2CookieKey returns the AES-256 key that seals one load balancer's
// session cookies and login state.
func elbv2CookieKey(lbArn string) ([]byte, error) {
	stored, err := elbv2AuthKeyFor("cookie/"+lbArn, func() (elbv2AuthKey, error) {
		key := make([]byte, 32)
		_, err := rand.Read(key)
		return elbv2AuthKey{ID: lbArn, Key: key}, err
	})
	return stored.Key, err
}

func elbv2AuthKeyFor(name string, generate func() (elbv2AuthKey, error)) (elbv2AuthKey, error) {
	elbv2AuthKeyMu.Lock()
	defer elbv2AuthKeyMu.Unlock()
	if key, ok := elbv2AuthKeys.Get(name); ok {
		return key, nil
	}
	key, err := generate()
	if err != nil {
		return key, err
	}
	elbv2AuthKeys.Put(name, key)
	return key, nil
}

func elbv2Seal(lbArn string, v any) (string, error) {
	key, err := elbv2CookieKey(lbArn)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	// "Application Load Balancers do not support cookie values that are URL
	// encoded", so the value is standard base64, every byte of which a cookie
	// value admits.
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, []byte(lbArn))), nil
}

func elbv2Open(lbArn, sealed string, v any) error {
	key, err := elbv2CookieKey(lbArn)
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(raw) < gcm.NonceSize() {
		return errors.New("sealed value too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(lbArn))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// elbv2AuthActionRef names the action a login and a session belong to: the
// listener, the rule (empty for the default actions) and the action's order.
type elbv2AuthActionRef struct {
	Listener string `json:"l"`
	Rule     string `json:"r,omitempty"`
	Order    int    `json:"o"`
}

// elbv2AuthIdP is the identity provider an authenticate action names, with
// the endpoints of an Amazon Cognito user pool's domain worked out.
type elbv2AuthIdP struct {
	Issuer, Authorization, Token, UserInfo string
	ClientID, ClientSecret, Scope          string
	CookieName                             string
	SessionTimeout                         int64
	OnUnauthenticated                      string
	ExtraParams                            map[string]string
}

func elbv2AuthIdPFor(action ELBv2Action) (elbv2AuthIdP, bool) {
	var idp elbv2AuthIdP
	var timeout *int64
	switch {
	case action.Type == "authenticate-oidc" && action.AuthOidc != nil:
		c := action.AuthOidc
		idp = elbv2AuthIdP{
			Issuer: c.Issuer, Authorization: c.AuthorizationEndpoint, Token: c.TokenEndpoint, UserInfo: c.UserInfoEndpoint,
			ClientID: c.ClientId, ClientSecret: c.ClientSecret, Scope: c.Scope, CookieName: c.SessionCookieName,
			OnUnauthenticated: c.OnUnauthenticatedRequest, ExtraParams: c.AuthenticationRequestExtraParams,
		}
		timeout = c.SessionTimeout
	case action.Type == "authenticate-cognito" && action.AuthCognito != nil:
		c := action.AuthCognito
		host := c.UserPoolDomain
		if !strings.Contains(host, ".") {
			host = cognitoPrefixDomainHost(host)
		}
		poolID := c.UserPoolArn[strings.LastIndex(c.UserPoolArn, "/")+1:]
		base := "https://" + host
		idp = elbv2AuthIdP{
			Issuer: cognitoIssuer(poolID), Authorization: base + "/oauth2/authorize", Token: base + "/oauth2/token",
			UserInfo: base + "/oauth2/userInfo", ClientID: c.UserPoolClientId, ClientSecret: c.ClientSecret, Scope: c.Scope,
			CookieName: c.SessionCookieName, OnUnauthenticated: c.OnUnauthenticatedRequest, ExtraParams: c.AuthenticationRequestExtraParams,
		}
		timeout = c.SessionTimeout
	default:
		return idp, false
	}
	// "The default is openid."
	idp.Scope = cmpOr(idp.Scope, "openid")
	idp.CookieName = cmpOr(idp.CookieName, elbv2AuthDefaultCookie)
	idp.OnUnauthenticated = cmpOr(idp.OnUnauthenticated, "authenticate")
	idp.SessionTimeout = elbv2AuthDefaultSessionTimeout
	if timeout != nil {
		idp.SessionTimeout = *timeout
	}
	return idp, true
}

func elbv2IsAuthAction(action ELBv2Action) bool {
	return action.Type == "authenticate-oidc" || action.Type == "authenticate-cognito"
}

// elbv2AuthActionsRefusal applies the rules the model and the guide state for
// authenticate actions: HTTPS listeners only, a routing action last, the
// required members and their values. previous carries the actions being
// replaced, whose client secret UseExistingClientSecret keeps. An
// authenticate-cognito action reads its app client's secret through
// DescribeUserPoolClient as the caller, which "you must grant the user
// permission to call".
func elbv2AuthActionsRefusal(r *http.Request, protocol string, actions, previous []ELBv2Action) (code, message string) {
	routing := 0
	for i := range actions {
		action := &actions[i]
		switch action.Type {
		case "forward", "fixed-response", "redirect":
			routing++
			continue
		case "authenticate-oidc", "authenticate-cognito":
		default:
			continue
		}
		if !strings.EqualFold(protocol, "HTTPS") {
			return "InvalidLoadBalancerAction", "The action type '" + action.Type + "' is supported only on HTTPS listeners"
		}
		if action.AuthOidc == nil && action.AuthCognito == nil {
			return "ValidationError", "An " + action.Type + " action requires its configuration"
		}
		var unauthenticated string
		if c := action.AuthOidc; c != nil {
			unauthenticated = c.OnUnauthenticatedRequest
			for name, endpoint := range map[string]string{"Issuer": c.Issuer, "AuthorizationEndpoint": c.AuthorizationEndpoint,
				"TokenEndpoint": c.TokenEndpoint, "UserInfoEndpoint": c.UserInfoEndpoint} {
				// "This must be a full URL, including the HTTPS protocol, the
				// domain, and the path."
				if u, err := url.Parse(endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
					return "InvalidLoadBalancerAction", "The " + name + " must be a full URL, including the HTTPS protocol, the domain, and the path"
				}
			}
			if c.ClientId == "" {
				return "ValidationError", "The ClientId of an authenticate-oidc action is required"
			}
			if c.UseExistingClientSecret {
				for _, old := range previous {
					if old.Type == "authenticate-oidc" && old.AuthOidc != nil && old.AuthOidc.ClientId == c.ClientId {
						c.ClientSecret = old.AuthOidc.ClientSecret
					}
				}
			}
			if c.ClientSecret == "" {
				return "ValidationError", "The ClientSecret of an authenticate-oidc action is required unless UseExistingClientSecret keeps the rule's existing secret"
			}
		}
		if c := action.AuthCognito; c != nil {
			unauthenticated = c.OnUnauthenticatedRequest
			if c.UserPoolArn == "" || c.UserPoolClientId == "" || c.UserPoolDomain == "" {
				return "ValidationError", "UserPoolArn, UserPoolClientId and UserPoolDomain of an authenticate-cognito action are required"
			}
			secret, refusal := elbv2CognitoClientSecret(r, c)
			if refusal != "" {
				return "InvalidLoadBalancerAction", refusal
			}
			c.ClientSecret = secret
		}
		switch unauthenticated {
		case "", "deny", "allow", "authenticate":
		default:
			return "ValidationError", "OnUnauthenticatedRequest must be one of deny, allow, authenticate"
		}
		for _, timeout := range []*int64{elbv2AuthTimeout(*action)} {
			if timeout != nil && (*timeout < 1 || *timeout > elbv2AuthDefaultSessionTimeout) {
				return "ValidationError", "SessionTimeout must be between 1 and 604800 seconds"
			}
		}
		if len(elbv2AuthExtraParams(*action)) > 10 {
			return "ValidationError", "AuthenticationRequestExtraParams admits up to 10 parameters"
		}
		elbv2AuthDefaults(action)
	}
	if routing > 1 {
		return "InvalidLoadBalancerAction", "Each rule must include exactly one of forward, fixed-response, or redirect"
	}
	if len(actions) > 0 && !slicesContainsRouting(actions[len(actions)-1]) && elbv2HasAuthAction(actions) {
		return "InvalidLoadBalancerAction", "The routing action must be the last action to be performed"
	}
	return "", ""
}

// elbv2AuthDefaults records the defaults the model states, which the
// load balancer reports back: scope openid, cookie AWSELBAuthSessionCookie, a
// 604800-second session, and authenticate for an unauthenticated request.
func elbv2AuthDefaults(action *ELBv2Action) {
	timeout := int64(elbv2AuthDefaultSessionTimeout)
	if c := action.AuthOidc; c != nil {
		c.Scope = cmpOr(c.Scope, "openid")
		c.SessionCookieName = cmpOr(c.SessionCookieName, elbv2AuthDefaultCookie)
		c.OnUnauthenticatedRequest = cmpOr(c.OnUnauthenticatedRequest, "authenticate")
		if c.SessionTimeout == nil {
			c.SessionTimeout = &timeout
		}
	}
	if c := action.AuthCognito; c != nil {
		c.Scope = cmpOr(c.Scope, "openid")
		c.SessionCookieName = cmpOr(c.SessionCookieName, elbv2AuthDefaultCookie)
		c.OnUnauthenticatedRequest = cmpOr(c.OnUnauthenticatedRequest, "authenticate")
		if c.SessionTimeout == nil {
			c.SessionTimeout = &timeout
		}
	}
}

func slicesContainsRouting(a ELBv2Action) bool {
	return a.Type == "forward" || a.Type == "fixed-response" || a.Type == "redirect"
}

func elbv2HasAuthAction(actions []ELBv2Action) bool {
	for _, a := range actions {
		if elbv2IsAuthAction(a) {
			return true
		}
	}
	return false
}

func elbv2AuthTimeout(a ELBv2Action) *int64 {
	if a.AuthOidc != nil {
		return a.AuthOidc.SessionTimeout
	}
	if a.AuthCognito != nil {
		return a.AuthCognito.SessionTimeout
	}
	return nil
}

func elbv2AuthExtraParams(a ELBv2Action) map[string]string {
	if a.AuthOidc != nil {
		return a.AuthOidc.AuthenticationRequestExtraParams
	}
	if a.AuthCognito != nil {
		return a.AuthCognito.AuthenticationRequestExtraParams
	}
	return nil
}

// elbv2CognitoClientSecret reads the app client through Amazon Cognito's
// DescribeUserPoolClient, signed with the caller's own credential, so the
// call succeeds only when the caller holds the permission.
func elbv2CognitoClientSecret(r *http.Request, c *ELBv2AuthCognitoConfig) (string, string) {
	poolID := c.UserPoolArn[strings.LastIndex(c.UserPoolArn, "/")+1:]
	body, err := json.Marshal(map[string]string{"UserPoolId": poolID, "ClientId": c.UserPoolClientId})
	if err != nil {
		return "", err.Error()
	}
	akid := iamAccessKeyIDFromRequest(r)
	secret, serr := sigv4SecretFor(akid, sigv4PresentedSessionToken(r))
	if serr != nil {
		return "", "The caller's credential cannot read the user pool client"
	}
	req, err := http.NewRequestWithContext(sfnAWSServer.RequestContext(r.Context()), http.MethodPost, "http://"+awsCallHost+"/", bytes.NewReader(body))
	if err != nil {
		return "", err.Error()
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService.DescribeUserPoolClient")
	sigv4Sign(req, body, IAMTempCred{AccessKeyID: akid, SecretAccessKey: secret, SessionToken: sigv4PresentedSessionToken(r)}, "cognito-idp", time.Now())
	resp := sfnAWSServer.Call(req)
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(resp.Body.Bytes(), &failure)
		return "", fmt.Sprintf("Amazon Cognito refused DescribeUserPoolClient for client %s in user pool %s: %s %s", c.UserPoolClientId, poolID, failure.Type, failure.Message)
	}
	var described struct {
		UserPoolClient struct {
			ClientSecret string `json:"ClientSecret"`
		} `json:"UserPoolClient"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &described); err != nil {
		return "", err.Error()
	}
	if described.UserPoolClient.ClientSecret == "" {
		return "", "The user pool client " + c.UserPoolClientId + " must have a client secret"
	}
	return described.UserPoolClient.ClientSecret, ""
}

// elbv2AuthState is what the state parameter carries, sealed with the load
// balancer's key, from the redirect to the IdP back to /oauth2/idpresponse.
type elbv2AuthState struct {
	Action  elbv2AuthActionRef `json:"a"`
	Nonce   string             `json:"n"`
	Return  string             `json:"u"`
	Created int64              `json:"t"`
}

// elbv2AuthSession is the session cookie's sealed content.
type elbv2AuthSession struct {
	Action        elbv2AuthActionRef `json:"a"`
	Claims        map[string]any     `json:"c"`
	AccessToken   string             `json:"t"`
	RefreshToken  string             `json:"r,omitempty"`
	AccessExpires int64              `json:"e"`
	Expires       int64              `json:"s"`
}

// elbv2AuthResult is what an authenticate action decided for a request.
type elbv2AuthResult int

const (
	elbv2AuthAnswered elbv2AuthResult = iota
	elbv2AuthContinue
)

// elbv2Authenticate runs one authenticate action: it continues with the
// session's claims on the request, or answers the request itself.
func elbv2Authenticate(w http.ResponseWriter, r *http.Request, listener ELBv2Listener, ref elbv2AuthActionRef, action ELBv2Action) elbv2AuthResult {
	idp, ok := elbv2AuthIdPFor(action)
	if !ok {
		http.Error(w, "authenticate action without configuration", http.StatusInternalServerError)
		return elbv2AuthAnswered
	}
	session, present, valid := elbv2ReadSession(r, listener, idp, ref)
	if valid && time.Now().Unix() >= session.AccessExpires && session.RefreshToken != "" {
		// "the load balancer saves the refresh token and uses it to refresh the
		// user claims each time the access token expires ... If the user logs
		// out, the refresh fails and the load balancer redirects the user to the
		// IdP authorization endpoint."
		refreshed, err := elbv2RefreshSession(r.Context(), idp, session)
		if err != nil {
			valid = false
		} else {
			session = refreshed
			if err := elbv2WriteSession(w, r, listener, idp, session); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return elbv2AuthAnswered
			}
		}
	}
	if valid {
		if err := elbv2SetClaimsHeaders(r, listener, idp, session); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return elbv2AuthAnswered
		}
		return elbv2AuthContinue
	}
	switch idp.OnUnauthenticated {
	case "allow":
		return elbv2AuthContinue
	case "deny":
		// "the load balancer returns an HTTP 401 Unauthorized error to AJAX
		// calls that have no authentication information. But if the user has
		// expired authentication information, it redirects the client to the
		// IdP authorization endpoint."
		if !present {
			http.Error(w, "401 Authorization Required", http.StatusUnauthorized)
			return elbv2AuthAnswered
		}
	}
	elbv2RedirectToIdP(w, r, listener, idp, ref)
	return elbv2AuthAnswered
}

func elbv2RedirectToIdP(w http.ResponseWriter, r *http.Request, listener ELBv2Listener, idp elbv2AuthIdP, ref elbv2AuthActionRef) {
	nonce := lbplane.RandomHex(16)
	state, err := elbv2Seal(listener.LoadBalancerArn, elbv2AuthState{Action: ref, Nonce: nonce, Return: r.URL.RequestURI(), Created: time.Now().Unix()})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	query := url.Values{}
	for k, v := range idp.ExtraParams {
		query.Set(k, v)
	}
	query.Set("response_type", "code")
	query.Set("client_id", idp.ClientID)
	query.Set("redirect_uri", "https://"+r.Host+elbv2AuthCallbackPath)
	query.Set("scope", idp.Scope)
	query.Set("state", state)
	http.SetCookie(w, &http.Cookie{
		Name: elbv2AuthNonceCookie, Value: nonce, Path: "/", Secure: true, HttpOnly: true,
		Expires: time.Now().Add(elbv2AuthLoginWindow),
	})
	target, err := url.Parse(idp.Authorization)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	existing := target.Query()
	for k, v := range query {
		existing[k] = v
	}
	target.RawQuery = existing.Encode()
	w.Header().Set("Location", target.String())
	w.WriteHeader(http.StatusFound)
}

// elbv2ServeAuthCallback answers /oauth2/idpresponse: it checks the state the
// redirect sealed, redeems the code, reads the user's claims and sets the
// session cookie, then sends the browser back where it started.
func elbv2ServeAuthCallback(w http.ResponseWriter, r *http.Request, listener ELBv2Listener) bool {
	if r.URL.Path != elbv2AuthCallbackPath || !strings.EqualFold(listener.Protocol, "HTTPS") {
		return false
	}
	query := r.URL.Query()
	var state elbv2AuthState
	if err := elbv2Open(listener.LoadBalancerArn, query.Get("state"), &state); err != nil || state.Action.Listener != listener.Arn {
		return false
	}
	unauthorized := func() { http.Error(w, "401 Authorization Required", http.StatusUnauthorized) }
	if time.Since(time.Unix(state.Created, 0)) > elbv2AuthLoginWindow {
		unauthorized()
		return true
	}
	if cookie, err := r.Cookie(elbv2AuthNonceCookie); err != nil || cookie.Value != state.Nonce {
		unauthorized()
		return true
	}
	action, ok := elbv2AuthActionFor(state.Action)
	if !ok {
		unauthorized()
		return true
	}
	idp, _ := elbv2AuthIdPFor(action)
	code := query.Get("code")
	if code == "" || query.Get("error") != "" {
		unauthorized()
		return true
	}
	tokens, err := elbv2TokenRequest(r.Context(), idp, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"https://" + r.Host + elbv2AuthCallbackPath},
		"client_id": {idp.ClientID},
	})
	if err != nil {
		unauthorized()
		return true
	}
	claims, err := elbv2UserInfo(r.Context(), idp, tokens.AccessToken)
	if err != nil {
		unauthorized()
		return true
	}
	now := time.Now()
	session := elbv2AuthSession{
		Action: state.Action, Claims: claims, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		AccessExpires: now.Add(time.Duration(tokens.ExpiresIn) * time.Second).Unix(),
		Expires:       now.Add(time.Duration(idp.SessionTimeout) * time.Second).Unix(),
	}
	if err := elbv2WriteSession(w, r, listener, idp, session); err != nil {
		if errors.Is(err, errELBv2ClaimsTooLarge) {
			elbv2RecordAuthClaimsSizeExceeded(listener.LoadBalancerArn)
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return true
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	http.SetCookie(w, &http.Cookie{Name: elbv2AuthNonceCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true})
	w.Header().Set("Location", "https://"+r.Host+state.Return)
	w.WriteHeader(http.StatusFound)
	return true
}

// elbv2AuthActionFor finds the action a login or session names in the
// listener's current configuration.
func elbv2AuthActionFor(ref elbv2AuthActionRef) (ELBv2Action, bool) {
	var actions []ELBv2Action
	if ref.Rule == "" {
		listener, ok := elbv2Listeners.Get(ref.Listener)
		if !ok {
			return ELBv2Action{}, false
		}
		actions = listener.DefaultActions
	} else {
		rule, ok := elbv2Rules.Get(ref.Rule)
		if !ok || rule.ListenerArn != ref.Listener {
			return ELBv2Action{}, false
		}
		actions = rule.Actions
	}
	for _, a := range actions {
		if a.Order == ref.Order && elbv2IsAuthAction(a) {
			return a, true
		}
	}
	return ELBv2Action{}, false
}

var errELBv2ClaimsTooLarge = errors.New("user claims and access token exceed 11K")

// elbv2WriteSession seals the session into the cookie, sharded into
// <name>-0, <name>-1, ... of at most 4K each, and expires any shard the new
// value no longer needs.
func elbv2WriteSession(w http.ResponseWriter, r *http.Request, listener ELBv2Listener, idp elbv2AuthIdP, session elbv2AuthSession) error {
	claims, err := json.Marshal(session.Claims)
	if err != nil {
		return err
	}
	if len(claims)+len(session.AccessToken) > elbv2AuthClaimsLimit {
		return errELBv2ClaimsTooLarge
	}
	sealed, err := elbv2Seal(listener.LoadBalancerArn, session)
	if err != nil {
		return err
	}
	expires := time.Now().Add(elbv2AuthCookieLifetime)
	// "This cookie contains the SameSite=None attribute with CORS (cross-origin
	// resource sharing) requests."
	sameSite := http.SameSiteDefaultMode
	if r.Header.Get("Origin") != "" {
		sameSite = http.SameSiteNoneMode
	}
	shards := 0
	for start := 0; start < len(sealed); start += elbv2AuthCookieShard {
		end := min(start+elbv2AuthCookieShard, len(sealed))
		http.SetCookie(w, &http.Cookie{
			Name: idp.CookieName + "-" + strconv.Itoa(shards), Value: sealed[start:end], Path: "/",
			Expires: expires, Secure: true, SameSite: sameSite,
		})
		shards++
	}
	for i := shards; ; i++ {
		if _, err := r.Cookie(idp.CookieName + "-" + strconv.Itoa(i)); err != nil {
			break
		}
		http.SetCookie(w, &http.Cookie{Name: idp.CookieName + "-" + strconv.Itoa(i), Value: "", Path: "/", MaxAge: -1, Secure: true})
	}
	return nil
}

// elbv2ReadSession reassembles and opens the session cookie. present reports
// whether the request carried one at all; valid, whether it is this action's
// and its session has not timed out.
func elbv2ReadSession(r *http.Request, listener ELBv2Listener, idp elbv2AuthIdP, ref elbv2AuthActionRef) (session elbv2AuthSession, present, valid bool) {
	var sealed strings.Builder
	for i := 0; ; i++ {
		cookie, err := r.Cookie(idp.CookieName + "-" + strconv.Itoa(i))
		if err != nil {
			break
		}
		sealed.WriteString(cookie.Value)
	}
	if sealed.Len() == 0 {
		return session, false, false
	}
	if err := elbv2Open(listener.LoadBalancerArn, sealed.String(), &session); err != nil {
		return session, true, false
	}
	return session, true, session.Action == ref && time.Now().Unix() < session.Expires
}

type elbv2TokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func elbv2TokenRequest(ctx context.Context, idp elbv2AuthIdP, form url.Values) (elbv2TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, idp.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return elbv2TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(idp.ClientID), url.QueryEscape(idp.ClientSecret))
	status, body, err := elbv2IdPDo(req)
	if err != nil {
		return elbv2TokenResponse{}, err
	}
	if status != http.StatusOK {
		return elbv2TokenResponse{}, fmt.Errorf("token endpoint answered %d: %s", status, body)
	}
	var tokens elbv2TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return tokens, err
	}
	if tokens.AccessToken == "" {
		return tokens, errors.New("token endpoint returned no access token")
	}
	if tokens.ExpiresIn <= 0 {
		tokens.ExpiresIn = 3600
	}
	return tokens, nil
}

func elbv2UserInfo(ctx context.Context, idp elbv2AuthIdP, accessToken string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, idp.UserInfo, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	status, body, err := elbv2IdPDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("user info endpoint answered %d", status)
	}
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func elbv2RefreshSession(ctx context.Context, idp elbv2AuthIdP, session elbv2AuthSession) (elbv2AuthSession, error) {
	tokens, err := elbv2TokenRequest(ctx, idp, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {session.RefreshToken}, "client_id": {idp.ClientID}})
	if err != nil {
		return session, err
	}
	claims, err := elbv2UserInfo(ctx, idp, tokens.AccessToken)
	if err != nil {
		return session, err
	}
	session.Claims = claims
	session.AccessToken = tokens.AccessToken
	session.AccessExpires = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).Unix()
	if tokens.RefreshToken != "" {
		session.RefreshToken = tokens.RefreshToken
	}
	return session, nil
}

// elbv2IdPDo sends a request to an IdP endpoint. An Amazon Cognito endpoint
// is served by this simulator, so the request goes through its handler chain
// in-process; any other IdP is reached over the network.
func elbv2IdPDo(req *http.Request) (int, []byte, error) {
	if host := lbplane.Hostname(req.URL.Host); strings.HasSuffix(host, cognitoPrefixDomainSuffix()) || host == cognitoIssuerHost() {
		req = req.WithContext(sfnAWSServer.RequestContext(req.Context()))
		req.Host = req.URL.Host
		resp := sfnAWSServer.Call(req)
		return resp.StatusCode, resp.Body.Bytes(), nil
	}
	resp, err := elbv2IdPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

var elbv2IdPClient = &http.Client{Timeout: 30 * time.Second}

// elbv2SetClaimsHeaders replaces whatever x-amzn-oidc-* headers the client
// sent with the session's: the access token, the subject and the signed
// claims.
func elbv2SetClaimsHeaders(r *http.Request, listener ELBv2Listener, idp elbv2AuthIdP, session elbv2AuthSession) error {
	data, err := elbv2SignClaims(listener.LoadBalancerArn, idp, session)
	if err != nil {
		return err
	}
	sub, _ := session.Claims["sub"].(string)
	r.Header.Set("X-Amzn-Oidc-Accesstoken", session.AccessToken)
	r.Header.Set("X-Amzn-Oidc-Identity", sub)
	r.Header.Set("X-Amzn-Oidc-Data", data)
	return nil
}

// elbv2SignClaims renders x-amzn-oidc-data: a JWT whose header carries alg,
// kid, signer, iss, client and exp and whose payload is the user info
// claims, "base64 URL encoded, and includes padding characters at the end",
// signed with ES256.
func elbv2SignClaims(lbArn string, idp elbv2AuthIdP, session elbv2AuthSession) (string, error) {
	key, kid, err := elbv2ClaimsSigningKey()
	if err != nil {
		return "", err
	}
	header, err := json.Marshal(map[string]any{
		"alg": "ES256", "kid": kid, "signer": lbArn, "iss": idp.Issuer, "client": idp.ClientID, "exp": session.AccessExpires,
	})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(session.Claims)
	if err != nil {
		return "", err
	}
	signing := base64.URLEncoding.EncodeToString(header) + "." + base64.URLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	rInt, sInt, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	rInt.FillBytes(signature[:32])
	sInt.FillBytes(signature[32:])
	return signing + "." + base64.URLEncoding.EncodeToString(signature), nil
}
