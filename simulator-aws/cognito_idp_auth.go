package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

// cognitoAuthSession is the Session a challenge hands the client, which the
// next RespondToAuthChallenge returns.
type cognitoAuthSession struct {
	ClientId  string
	Username  string
	Challenge string
	Expires   time.Time
}

var (
	cognitoAuthSessions sim.Store[cognitoAuthSession]
	cognitoClientsByID  sim.GenerationIndex[CognitoUserPoolClient]
	cognitoAdminScope   = []string{"aws.cognito.signin.user.admin"}
)

func registerCognitoAuthentication(r *AWSRouter, srv *sim.Server) {
	cognitoAuthSessions = sim.MakeStore[cognitoAuthSession](srv.DB(), "cognito_auth_sessions")
	r.Register("AWSCognitoIdentityProviderService.InitiateAuth", handleCognitoInitiateAuth)
	r.Register("AWSCognitoIdentityProviderService.AdminInitiateAuth", handleCognitoAdminInitiateAuth)
	r.Register("AWSCognitoIdentityProviderService.RespondToAuthChallenge", handleCognitoRespondToAuthChallenge)
	r.Register("AWSCognitoIdentityProviderService.AdminRespondToAuthChallenge", handleCognitoAdminRespondToAuthChallenge)
	r.Register("AWSCognitoIdentityProviderService.GetUser", handleCognitoGetUser)
}

// cognitoClientByID finds an app client by its ID alone, as InitiateAuth
// names it.
func cognitoClientByID(clientID string) (CognitoUserPoolClient, bool) {
	return cognitoClientsByID.Lookup(cognitoClients, clientID, func(c CognitoUserPoolClient) []string {
		return []string{c.ClientId}
	})
}

// cognitoSecretHash is "Base64 ( HMAC_SHA256 ( "Client Secret Key",
// "Username" + "Client Id" ) )".
func cognitoSecretHash(client CognitoUserPoolClient, username string) string {
	mac := hmac.New(sha256.New, []byte(client.ClientSecret))
	mac.Write([]byte(username + client.ClientId))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func cognitoSecretHashMatches(client CognitoUserPoolClient, username, presented string) bool {
	if client.ClientSecret == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(cognitoSecretHash(client, username)), []byte(presented)) == 1
}

type cognitoAPIAuthRequest struct {
	UserPoolId         string            `json:"UserPoolId"`
	ClientId           string            `json:"ClientId"`
	AuthFlow           string            `json:"AuthFlow"`
	AuthParameters     map[string]string `json:"AuthParameters"`
	ChallengeName      string            `json:"ChallengeName"`
	ChallengeResponses map[string]string `json:"ChallengeResponses"`
	Session            string            `json:"Session"`
	AccessToken        string            `json:"AccessToken"`
}

func handleCognitoInitiateAuth(w http.ResponseWriter, r *http.Request) {
	var req cognitoAPIAuthRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	client, ok := cognitoClientByID(req.ClientId)
	if !ok {
		cognitoBadRequest(w, "ResourceNotFoundException", "User pool client "+req.ClientId+" does not exist.")
		return
	}
	cognitoInitiateAuth(w, req, client, map[string]string{
		"USER_PASSWORD_AUTH": "ALLOW_USER_PASSWORD_AUTH",
	}, map[string]string{"USER_PASSWORD_AUTH": "USER_PASSWORD_AUTH"})
}

func handleCognitoAdminInitiateAuth(w http.ResponseWriter, r *http.Request) {
	var req cognitoAPIAuthRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	client, ok := cognitoClientFor(w, req.UserPoolId, req.ClientId)
	if !ok {
		return
	}
	cognitoInitiateAuth(w, req, client, map[string]string{
		"ADMIN_USER_PASSWORD_AUTH": "ALLOW_ADMIN_USER_PASSWORD_AUTH",
		"ADMIN_NO_SRP_AUTH":        "ALLOW_ADMIN_USER_PASSWORD_AUTH",
	}, map[string]string{"ADMIN_USER_PASSWORD_AUTH": "ADMIN_NO_SRP_AUTH", "ADMIN_NO_SRP_AUTH": "ADMIN_NO_SRP_AUTH"})
}

// cognitoInitiateAuth runs a password flow, which the client allows through
// passwordFlows' ExplicitAuthFlows value or its legacy one, or a refresh.
func cognitoInitiateAuth(w http.ResponseWriter, req cognitoAPIAuthRequest, client CognitoUserPoolClient, passwordFlows, legacyFlows map[string]string) {
	pool, ok := cognitoPool(w, client.UserPoolId)
	if !ok {
		return
	}
	switch flow := req.AuthFlow; {
	case flow == "REFRESH_TOKEN_AUTH" || flow == "REFRESH_TOKEN":
		if !slices.Contains(client.ExplicitAuthFlows, "ALLOW_REFRESH_TOKEN_AUTH") {
			cognitoBadRequest(w, "InvalidParameterException", "Refresh Token flow not enabled for this client")
			return
		}
		cognitoRefreshAuth(w, req, pool, client)
	case passwordFlows[flow] != "":
		if !slices.Contains(client.ExplicitAuthFlows, passwordFlows[flow]) && !slices.Contains(client.ExplicitAuthFlows, legacyFlows[flow]) {
			cognitoBadRequest(w, "InvalidParameterException", flow+" flow not enabled for this client")
			return
		}
		cognitoPasswordAuth(w, req, pool, client)
	default:
		cognitoBadRequest(w, "InvalidParameterException", "Unsupported authentication flow "+flow+".")
	}
}

func cognitoPasswordAuth(w http.ResponseWriter, req cognitoAPIAuthRequest, pool CognitoUserPool, client CognitoUserPoolClient) {
	username, password := req.AuthParameters["USERNAME"], req.AuthParameters["PASSWORD"]
	if username == "" || password == "" {
		cognitoBadRequest(w, "InvalidParameterException", "Missing required parameter USERNAME or PASSWORD")
		return
	}
	if !cognitoSecretHashMatches(client, username, req.AuthParameters["SECRET_HASH"]) {
		cognitoBadRequest(w, "NotAuthorizedException", "Unable to verify secret hash for client "+client.ClientId)
		return
	}
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, username))
	if !ok {
		if client.PreventUserExistenceErrors == "ENABLED" {
			cognitoBadRequest(w, "NotAuthorizedException", "Incorrect username or password.")
			return
		}
		cognitoBadRequest(w, "UserNotFoundException", "User does not exist.")
		return
	}
	if !cognitoPasswordMatches(user, password) {
		cognitoBadRequest(w, "NotAuthorizedException", "Incorrect username or password.")
		return
	}
	switch {
	case !user.Enabled:
		cognitoBadRequest(w, "NotAuthorizedException", "User is disabled.")
		return
	case user.Status == "RESET_REQUIRED":
		cognitoBadRequest(w, "PasswordResetRequiredException", "Password reset required for the user")
		return
	case user.Status == "UNCONFIRMED":
		cognitoBadRequest(w, "UserNotConfirmedException", "User is not confirmed.")
		return
	case user.Status == "FORCE_CHANGE_PASSWORD":
		if time.Now().After(user.TemporaryExpires) {
			cognitoBadRequest(w, "NotAuthorizedException", "Temporary password has expired and must be reset by an administrator.")
			return
		}
		session := cognitoRandom(cognitoAlnum, 88)
		cognitoAuthSessions.Prune(func(s cognitoAuthSession) bool { return time.Now().After(s.Expires) })
		cognitoAuthSessions.Put(pool.Id+"/"+session, cognitoAuthSession{
			ClientId: client.ClientId, Username: user.Username, Challenge: "NEW_PASSWORD_REQUIRED",
			// "AuthSessionValidity is the duration, in minutes, of that session
			// token."
			Expires: time.Now().Add(time.Duration(client.AuthSessionValidity) * time.Minute),
		})
		attributes := map[string]string{}
		for _, a := range user.Attributes {
			if a.Name != "sub" {
				attributes[a.Name] = a.Value
			}
		}
		userAttributes, err := json.Marshal(attributes)
		if err != nil {
			AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
			return
		}
		sub, _ := cognitoUserAttributes(user.Attributes, "sub")
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"ChallengeName": "NEW_PASSWORD_REQUIRED",
			"Session":       session,
			"ChallengeParameters": map[string]string{
				"USER_ID_FOR_SRP": sub, "requiredAttributes": "[]", "userAttributes": string(userAttributes),
			},
		})
		return
	}
	cognitoWriteAuthResult(w, pool, client, user)
}

// cognitoWriteAuthResult answers a completed sign-in with ID, access and
// refresh tokens; the access token's scope is aws.cognito.signin.user.admin.
func cognitoWriteAuthResult(w http.ResponseWriter, pool CognitoUserPool, client CognitoUserPoolClient, user CognitoUser) {
	originJTI := uuid.NewString()
	now := time.Now()
	tokens, err := cognitoIssueTokens(pool, client, user, cognitoAdminScope, true, "", now, originJTI)
	if err != nil {
		AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
		return
	}
	refresh := cognitoRandom(cognitoAlnum, 64)
	cognitoRefreshTokens.Prune(func(t cognitoRefreshToken) bool { return now.After(t.Expires) })
	cognitoRefreshTokens.Put(pool.Id+"/"+refresh, cognitoRefreshToken{
		WithID: true, ClientId: client.ClientId, Username: user.Username, Scopes: cognitoAdminScope, OriginJTI: originJTI, AuthTime: now,
		Expires: now.Add(time.Duration(cognitoSeconds(client.RefreshTokenValidity, client.TokenValidityUnits.RefreshToken, "days")) * time.Second),
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ChallengeParameters": map[string]string{},
		"AuthenticationResult": map[string]any{
			"AccessToken": tokens.access, "IdToken": tokens.id, "RefreshToken": refresh,
			"ExpiresIn": tokens.expiresIn, "TokenType": "Bearer",
		},
	})
}

func cognitoRefreshAuth(w http.ResponseWriter, req cognitoAPIAuthRequest, pool CognitoUserPool, client CognitoUserPoolClient) {
	refresh, ok := cognitoRefreshTokens.Get(pool.Id + "/" + req.AuthParameters["REFRESH_TOKEN"])
	if !ok || refresh.ClientId != client.ClientId || time.Now().After(refresh.Expires) {
		cognitoBadRequest(w, "NotAuthorizedException", "Invalid Refresh Token")
		return
	}
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, refresh.Username))
	if !ok || !user.Enabled {
		cognitoBadRequest(w, "NotAuthorizedException", "Invalid Refresh Token")
		return
	}
	// "When username is a sign-in attribute, set the secret hash username
	// value from the username claim."
	if !cognitoSecretHashMatches(client, user.Username, req.AuthParameters["SECRET_HASH"]) {
		cognitoBadRequest(w, "NotAuthorizedException", "Unable to verify secret hash for client "+client.ClientId)
		return
	}
	tokens, err := cognitoIssueTokens(pool, client, user, refresh.Scopes, refresh.WithID || slices.Contains(refresh.Scopes, "openid"), "", refresh.AuthTime, refresh.OriginJTI)
	if err != nil {
		AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
		return
	}
	result := map[string]any{"AccessToken": tokens.access, "ExpiresIn": tokens.expiresIn, "TokenType": "Bearer"}
	if tokens.id != "" {
		result["IdToken"] = tokens.id
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ChallengeParameters": map[string]string{}, "AuthenticationResult": result})
}

func handleCognitoRespondToAuthChallenge(w http.ResponseWriter, r *http.Request) {
	var req cognitoAPIAuthRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	client, ok := cognitoClientByID(req.ClientId)
	if !ok {
		cognitoBadRequest(w, "ResourceNotFoundException", "User pool client "+req.ClientId+" does not exist.")
		return
	}
	cognitoRespondToChallenge(w, req, client)
}

func handleCognitoAdminRespondToAuthChallenge(w http.ResponseWriter, r *http.Request) {
	var req cognitoAPIAuthRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	client, ok := cognitoClientFor(w, req.UserPoolId, req.ClientId)
	if !ok {
		return
	}
	cognitoRespondToChallenge(w, req, client)
}

// cognitoRespondToChallenge completes NEW_PASSWORD_REQUIRED: the user sets a
// password the pool's policy accepts and is signed in.
func cognitoRespondToChallenge(w http.ResponseWriter, req cognitoAPIAuthRequest, client CognitoUserPoolClient) {
	pool, ok := cognitoPool(w, client.UserPoolId)
	if !ok {
		return
	}
	if req.ChallengeName != "NEW_PASSWORD_REQUIRED" {
		cognitoBadRequest(w, "InvalidParameterException", "Unsupported challenge "+req.ChallengeName+".")
		return
	}
	key := pool.Id + "/" + req.Session
	session, ok := cognitoAuthSessions.Get(key)
	if !ok || session.ClientId != client.ClientId || session.Challenge != req.ChallengeName || time.Now().After(session.Expires) {
		cognitoBadRequest(w, "NotAuthorizedException", "Invalid session for the user, session is expired.")
		return
	}
	username := req.ChallengeResponses["USERNAME"]
	if cognitoUserKey(pool, username) != cognitoUserKey(pool, session.Username) {
		cognitoBadRequest(w, "NotAuthorizedException", "Invalid session for the user.")
		return
	}
	if !cognitoSecretHashMatches(client, username, req.ChallengeResponses["SECRET_HASH"]) {
		cognitoBadRequest(w, "NotAuthorizedException", "Unable to verify secret hash for client "+client.ClientId)
		return
	}
	password := req.ChallengeResponses["NEW_PASSWORD"]
	if password == "" {
		cognitoBadRequest(w, "InvalidParameterException", "Missing required parameter NEW_PASSWORD")
		return
	}
	if msg := cognitoPasswordViolation(pool.PasswordPolicy, password); msg != "" {
		cognitoBadRequest(w, "InvalidPasswordException", "Password does not conform to policy: "+msg)
		return
	}
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, session.Username))
	if !ok || !user.Enabled {
		cognitoBadRequest(w, "NotAuthorizedException", "Invalid session for the user.")
		return
	}
	if err := cognitoSetPassword(&user, password); err != nil {
		AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
		return
	}
	user.Status = "CONFIRMED"
	user.TemporaryExpires = time.Time{}
	user.LastModified = time.Now().UTC()
	cognitoUsers.Put(cognitoUserKey(pool, user.Username), user)
	cognitoAuthSessions.Delete(key)
	cognitoWriteAuthResult(w, pool, client, user)
}

// handleCognitoGetUser answers the user an access token names; the token must
// carry the aws.cognito.signin.user.admin scope.
func handleCognitoGetUser(w http.ResponseWriter, r *http.Request) {
	var req cognitoAPIAuthRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	claims, pool, ok := cognitoAccessTokenClaims(req.AccessToken)
	if !ok {
		cognitoBadRequest(w, "NotAuthorizedException", "Invalid Access Token")
		return
	}
	scope, _ := claims["scope"].(string)
	username, _ := claims["username"].(string)
	user, found := cognitoUsers.Get(cognitoUserKey(pool, username))
	if !slices.Contains(strings.Fields(scope), "aws.cognito.signin.user.admin") || !found || !user.Enabled {
		cognitoBadRequest(w, "NotAuthorizedException", "Access Token does not have required scopes")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Username": user.Username, "UserAttributes": user.Attributes})
}

// cognitoAccessTokenClaims verifies an access token against the key of the
// pool its issuer names.
func cognitoAccessTokenClaims(token string) (map[string]any, CognitoUserPool, bool) {
	unverified, err := cognitoUnverifiedClaims(token)
	if err != nil {
		return nil, CognitoUserPool{}, false
	}
	iss, _ := unverified["iss"].(string)
	poolID := strings.TrimPrefix(iss, cognitoIssuer(""))
	pool, ok := cognitoPools.Get(poolID)
	if !ok || cognitoIssuer(pool.Id) != iss {
		return nil, pool, false
	}
	claims, err := cognitoVerify(pool, token)
	if err != nil || claims["token_use"] != "access" {
		return nil, pool, false
	}
	return claims, pool, true
}

func cognitoUnverifiedClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	return claims, json.Unmarshal(payload, &claims)
}
