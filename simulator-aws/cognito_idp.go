package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

// CognitoUserPool is an Amazon Cognito user pool. Config holds the members of
// the CreateUserPool or UpdateUserPool request the simulator keeps as given
// and reports back; the fields beside it are the ones the simulator acts on.
type CognitoUserPool struct {
	Id                 string
	Name               string
	Arn                string
	CreationDate       time.Time
	LastModifiedDate   time.Time
	PasswordPolicy     CognitoPasswordPolicy
	SignInPolicy       json.RawMessage `json:",omitempty"`
	DeletionProtection string
	MfaConfiguration   string
	CaseSensitive      bool
	Schema             []json.RawMessage
	Config             map[string]json.RawMessage
	MfaSettings        map[string]json.RawMessage
	Tags               map[string]string
	Domain             string
	// SigningKey is the PKCS #8 RSA key that signs the pool's tokens; KeyID
	// names it in the tokens' header and the pool's JWKS.
	SigningKey []byte
	KeyID      string
}

type CognitoPasswordPolicy struct {
	MinimumLength                 int  `json:"MinimumLength,omitempty"`
	RequireUppercase              bool `json:"RequireUppercase"`
	RequireLowercase              bool `json:"RequireLowercase"`
	RequireNumbers                bool `json:"RequireNumbers"`
	RequireSymbols                bool `json:"RequireSymbols"`
	PasswordHistorySize           int  `json:"PasswordHistorySize,omitempty"`
	TemporaryPasswordValidityDays int  `json:"TemporaryPasswordValidityDays"`
}

type CognitoUserPoolClient struct {
	UserPoolId                      string
	ClientId                        string
	ClientName                      string
	ClientSecret                    string
	CreationDate                    time.Time
	LastModifiedDate                time.Time
	RefreshTokenValidity            int
	AccessTokenValidity             int
	IdTokenValidity                 int
	TokenValidityUnits              CognitoTokenValidityUnits
	ReadAttributes                  []string
	WriteAttributes                 []string
	ExplicitAuthFlows               []string
	SupportedIdentityProviders      []string
	CallbackURLs                    []string
	LogoutURLs                      []string
	DefaultRedirectURI              string
	AllowedOAuthFlows               []string
	AllowedOAuthScopes              []string
	AllowedOAuthFlowsUserPoolClient bool
	PreventUserExistenceErrors      string
	EnableTokenRevocation           bool
	EnablePropagateUserContextData  bool
	AuthSessionValidity             int
	Config                          map[string]json.RawMessage
}

type CognitoTokenValidityUnits struct {
	AccessToken  string `json:"AccessToken,omitempty"`
	IdToken      string `json:"IdToken,omitempty"`
	RefreshToken string `json:"RefreshToken,omitempty"`
}

type CognitoUser struct {
	UserPoolId       string
	Username         string
	Attributes       []CognitoAttribute
	Created          time.Time
	LastModified     time.Time
	Enabled          bool
	Status           string
	PasswordHash     []byte
	PasswordSalt     []byte
	TemporaryExpires time.Time
}

type CognitoAttribute struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type CognitoDomain struct {
	Domain     string
	UserPoolId string
	Version    string
}

var (
	cognitoPools   sim.Store[CognitoUserPool]
	cognitoClients sim.Store[CognitoUserPoolClient]
	cognitoUsers   sim.Store[CognitoUser]
	cognitoDomains sim.Store[CognitoDomain]
)

func registerCognitoIdentityProvider(r *AWSRouter, srv *sim.Server) {
	cognitoPools = sim.MakeStore[CognitoUserPool](srv.DB(), "cognito_user_pools")
	cognitoClients = sim.MakeStore[CognitoUserPoolClient](srv.DB(), "cognito_user_pool_clients")
	cognitoUsers = sim.MakeStore[CognitoUser](srv.DB(), "cognito_users")
	cognitoDomains = sim.MakeStore[CognitoDomain](srv.DB(), "cognito_user_pool_domains")
	registerCognitoHostedPlane(srv)

	r.Register("AWSCognitoIdentityProviderService.CreateUserPool", handleCognitoCreateUserPool)
	r.Register("AWSCognitoIdentityProviderService.DescribeUserPool", handleCognitoDescribeUserPool)
	r.Register("AWSCognitoIdentityProviderService.UpdateUserPool", handleCognitoUpdateUserPool)
	r.Register("AWSCognitoIdentityProviderService.DeleteUserPool", handleCognitoDeleteUserPool)
	r.Register("AWSCognitoIdentityProviderService.ListUserPools", handleCognitoListUserPools)
	r.Register("AWSCognitoIdentityProviderService.GetUserPoolMfaConfig", handleCognitoGetUserPoolMfaConfig)
	r.Register("AWSCognitoIdentityProviderService.SetUserPoolMfaConfig", handleCognitoSetUserPoolMfaConfig)
	r.Register("AWSCognitoIdentityProviderService.CreateUserPoolClient", handleCognitoCreateUserPoolClient)
	r.Register("AWSCognitoIdentityProviderService.DescribeUserPoolClient", handleCognitoDescribeUserPoolClient)
	r.Register("AWSCognitoIdentityProviderService.UpdateUserPoolClient", handleCognitoUpdateUserPoolClient)
	r.Register("AWSCognitoIdentityProviderService.DeleteUserPoolClient", handleCognitoDeleteUserPoolClient)
	r.Register("AWSCognitoIdentityProviderService.ListUserPoolClients", handleCognitoListUserPoolClients)
	r.Register("AWSCognitoIdentityProviderService.CreateUserPoolDomain", handleCognitoCreateUserPoolDomain)
	r.Register("AWSCognitoIdentityProviderService.DescribeUserPoolDomain", handleCognitoDescribeUserPoolDomain)
	r.Register("AWSCognitoIdentityProviderService.DeleteUserPoolDomain", handleCognitoDeleteUserPoolDomain)
	r.Register("AWSCognitoIdentityProviderService.AdminCreateUser", handleCognitoAdminCreateUser)
	r.Register("AWSCognitoIdentityProviderService.AdminGetUser", handleCognitoAdminGetUser)
	r.Register("AWSCognitoIdentityProviderService.AdminDeleteUser", handleCognitoAdminDeleteUser)
	r.Register("AWSCognitoIdentityProviderService.AdminSetUserPassword", handleCognitoAdminSetUserPassword)
	r.Register("AWSCognitoIdentityProviderService.AdminEnableUser", handleCognitoAdminEnableUser)
	r.Register("AWSCognitoIdentityProviderService.AdminDisableUser", handleCognitoAdminDisableUser)
	r.Register("AWSCognitoIdentityProviderService.ListUsers", handleCognitoListUsers)
	r.Register("AWSCognitoIdentityProviderService.TagResource", handleCognitoTagResource)
	r.Register("AWSCognitoIdentityProviderService.UntagResource", handleCognitoUntagResource)
	r.Register("AWSCognitoIdentityProviderService.ListTagsForResource", handleCognitoListTagsForResource)
}

func cognitoTime(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

func cognitoBadRequest(w http.ResponseWriter, code, message string) {
	AWSError(w, code, message, http.StatusBadRequest)
}

func cognitoReadRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := sim.ReadJSON(r, v); err != nil {
		cognitoBadRequest(w, "InvalidParameterException", "Invalid request body")
		return false
	}
	return true
}

func cognitoPool(w http.ResponseWriter, id string) (CognitoUserPool, bool) {
	pool, ok := cognitoPools.Get(id)
	if !ok {
		cognitoBadRequest(w, "ResourceNotFoundException", fmt.Sprintf("User pool %s does not exist.", id))
	}
	return pool, ok
}

// cognitoRandom draws n characters from alphabet.
func cognitoRandom(alphabet string, n int) string {
	out := make([]byte, n)
	limit := big.NewInt(int64(len(alphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic(err)
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out)
}

const (
	cognitoAlnum      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	cognitoLowerAlnum = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// cognitoPoolMembers are the CreateUserPool members the simulator reads
// itself; every other member is kept in Config as the request sent it.
var cognitoPoolMembers = []string{"PoolName", "Policies", "UserPoolTags", "Schema", "MfaConfiguration", "DeletionProtection", "UserPoolId"}

type cognitoPoolRequest struct {
	PoolName   string `json:"PoolName"`
	UserPoolId string `json:"UserPoolId"`
	Policies   *struct {
		PasswordPolicy *CognitoPasswordPolicy `json:"PasswordPolicy"`
		SignInPolicy   json.RawMessage        `json:"SignInPolicy"`
	} `json:"Policies"`
	UserPoolTags       map[string]string `json:"UserPoolTags"`
	Schema             []json.RawMessage `json:"Schema"`
	MfaConfiguration   string            `json:"MfaConfiguration"`
	DeletionProtection string            `json:"DeletionProtection"`
}

var cognitoPoolNamePattern = regexp.MustCompile(`^[\w\s+=,.@-]+$`)

// cognitoApplyPoolRequest sets every member a CreateUserPool or UpdateUserPool
// request carries. UpdateUserPool resets a member it omits to its default, so
// both requests replace the whole configuration.
func cognitoApplyPoolRequest(pool *CognitoUserPool, req cognitoPoolRequest, raw map[string]json.RawMessage) string {
	policy := CognitoPasswordPolicy{TemporaryPasswordValidityDays: 7}
	pool.SignInPolicy = nil
	if req.Policies != nil {
		if req.Policies.PasswordPolicy != nil {
			policy = *req.Policies.PasswordPolicy
			if policy.MinimumLength != 0 && (policy.MinimumLength < 6 || policy.MinimumLength > 99) {
				return "1 validation error detected: Value at 'policies.passwordPolicy.minimumLength' failed to satisfy constraint: Member must have value between 6 and 99"
			}
			if policy.TemporaryPasswordValidityDays == 0 {
				policy.TemporaryPasswordValidityDays = 7
			}
		}
		pool.SignInPolicy = req.Policies.SignInPolicy
	}
	pool.PasswordPolicy = policy
	switch req.MfaConfiguration {
	case "":
		pool.MfaConfiguration = "OFF"
	case "OFF", "ON", "OPTIONAL":
		if req.MfaConfiguration != "OFF" {
			if _, ok := raw["SmsConfiguration"]; !ok {
				return "SMS configuration is required when MFA is required or optional."
			}
		}
		pool.MfaConfiguration = req.MfaConfiguration
	default:
		return "Invalid MFA configuration given."
	}
	switch req.DeletionProtection {
	case "":
		pool.DeletionProtection = "INACTIVE"
	case "ACTIVE", "INACTIVE":
		pool.DeletionProtection = req.DeletionProtection
	default:
		return "Invalid deletion protection given."
	}
	pool.Config = map[string]json.RawMessage{}
	for k, v := range raw {
		if !slices.Contains(cognitoPoolMembers, k) {
			pool.Config[k] = v
		}
	}
	return ""
}

func cognitoDecodeRaw(w http.ResponseWriter, r *http.Request, req any) (map[string]json.RawMessage, bool) {
	var raw map[string]json.RawMessage
	if !cognitoReadRequest(w, r, &raw) {
		return nil, false
	}
	body, err := json.Marshal(raw)
	if err == nil {
		err = json.Unmarshal(body, req)
	}
	if err != nil {
		cognitoBadRequest(w, "InvalidParameterException", "Invalid request body")
		return nil, false
	}
	return raw, true
}

func handleCognitoCreateUserPool(w http.ResponseWriter, r *http.Request) {
	var req cognitoPoolRequest
	raw, ok := cognitoDecodeRaw(w, r, &req)
	if !ok {
		return
	}
	if req.PoolName == "" || len(req.PoolName) > 128 || !cognitoPoolNamePattern.MatchString(req.PoolName) {
		cognitoBadRequest(w, "InvalidParameterException", "1 validation error detected: Value at 'poolName' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\w\\s+=,.@-]+")
		return
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
		return
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
		return
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
		return
	}
	kid := sha256.Sum256(public)
	now := time.Now().UTC()
	id := awsRegion() + "_" + cognitoRandom(cognitoAlnum, 9)
	pool := CognitoUserPool{
		Id:               id,
		Name:             req.PoolName,
		Arn:              fmt.Sprintf("arn:aws:cognito-idp:%s:%s:userpool/%s", awsRegion(), awsAccountID(), id),
		CreationDate:     now,
		LastModifiedDate: now,
		Schema:           req.Schema,
		CaseSensitive:    cognitoCaseSensitive(raw),
		Tags:             req.UserPoolTags,
		MfaSettings:      map[string]json.RawMessage{},
		SigningKey:       der,
		KeyID:            base64.StdEncoding.EncodeToString(kid[:]),
	}
	if msg := cognitoApplyPoolRequest(&pool, req, raw); msg != "" {
		cognitoBadRequest(w, "InvalidParameterException", msg)
		return
	}
	cognitoPools.Put(id, pool)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"UserPool": cognitoPoolJSON(pool)})
}

// cognitoCaseSensitive reads UsernameConfiguration.CaseSensitive; a pool
// created without it matches user names case-sensitively.
func cognitoCaseSensitive(raw map[string]json.RawMessage) bool {
	var config struct {
		CaseSensitive *bool `json:"CaseSensitive"`
	}
	if v, ok := raw["UsernameConfiguration"]; ok && json.Unmarshal(v, &config) == nil && config.CaseSensitive != nil {
		return *config.CaseSensitive
	}
	return true
}

func cognitoPoolJSON(pool CognitoUserPool) map[string]any {
	out := map[string]any{}
	for k, v := range pool.Config {
		out[k] = v
	}
	policies := map[string]any{"PasswordPolicy": pool.PasswordPolicy}
	if len(pool.SignInPolicy) > 0 {
		policies["SignInPolicy"] = pool.SignInPolicy
	}
	users := 0
	for range cognitoUsers.ListPrefix(pool.Id + "/") {
		users++
	}
	out["Id"] = pool.Id
	out["Name"] = pool.Name
	out["Arn"] = pool.Arn
	out["CreationDate"] = cognitoTime(pool.CreationDate)
	out["LastModifiedDate"] = cognitoTime(pool.LastModifiedDate)
	out["Policies"] = policies
	out["DeletionProtection"] = pool.DeletionProtection
	out["MfaConfiguration"] = pool.MfaConfiguration
	out["EstimatedNumberOfUsers"] = users
	out["SchemaAttributes"] = cognitoSchemaJSON(pool.Schema)
	if len(pool.Tags) > 0 {
		out["UserPoolTags"] = pool.Tags
	}
	if pool.Domain != "" {
		out["Domain"] = pool.Domain
	}
	return out
}

// cognitoSchemaJSON names each custom attribute the way DescribeUserPool does,
// with the custom: prefix the attribute carries on a user.
func cognitoSchemaJSON(schema []json.RawMessage) []map[string]any {
	out := make([]map[string]any, 0, len(schema))
	for _, raw := range schema {
		var attr map[string]any
		if json.Unmarshal(raw, &attr) != nil {
			continue
		}
		if name, ok := attr["Name"].(string); ok && !cognitoStandardAttributes[name] && !strings.HasPrefix(name, "custom:") {
			attr["Name"] = "custom:" + name
		}
		out = append(out, attr)
	}
	return out
}

// The standard attributes the OpenID Connect specification lists, which a
// user pool holds without a schema entry.
var cognitoStandardAttributes = map[string]bool{
	"sub": true, "name": true, "given_name": true, "family_name": true, "middle_name": true, "nickname": true,
	"preferred_username": true, "profile": true, "picture": true, "website": true, "email": true,
	"email_verified": true, "gender": true, "birthdate": true, "zoneinfo": true, "locale": true,
	"phone_number": true, "phone_number_verified": true, "address": true, "updated_at": true,
}

func handleCognitoDescribeUserPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolId string `json:"UserPoolId"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, ok := cognitoPool(w, req.UserPoolId)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"UserPool": cognitoPoolJSON(pool)})
}

func handleCognitoUpdateUserPool(w http.ResponseWriter, r *http.Request) {
	var req cognitoPoolRequest
	raw, ok := cognitoDecodeRaw(w, r, &req)
	if !ok {
		return
	}
	pool, ok := cognitoPool(w, req.UserPoolId)
	if !ok {
		return
	}
	if msg := cognitoApplyPoolRequest(&pool, req, raw); msg != "" {
		cognitoBadRequest(w, "InvalidParameterException", msg)
		return
	}
	if req.UserPoolTags != nil {
		pool.Tags = req.UserPoolTags
	}
	pool.LastModifiedDate = time.Now().UTC()
	cognitoPools.Put(pool.Id, pool)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCognitoDeleteUserPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolId string `json:"UserPoolId"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, ok := cognitoPool(w, req.UserPoolId)
	if !ok {
		return
	}
	if pool.DeletionProtection == "ACTIVE" {
		cognitoBadRequest(w, "InvalidParameterException", "The user pool cannot be deleted because deletion protection is activated. Deletion protection must be inactivated first.")
		return
	}
	if pool.Domain != "" {
		cognitoBadRequest(w, "InvalidParameterException", "User pool cannot be deleted. It has a domain configured that should be deleted first.")
		return
	}
	for _, kv := range cognitoClients.ListPrefix(pool.Id + "/") {
		cognitoClients.Delete(kv.ID)
	}
	for _, kv := range cognitoUsers.ListPrefix(pool.Id + "/") {
		cognitoUsers.Delete(kv.ID)
	}
	cognitoForgetPool(pool.Id)
	cognitoPools.Delete(pool.Id)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// cognitoPage pages a sorted list by an opaque token holding the next index.
func cognitoPage[T any](items []T, token string, limit int) ([]T, string, bool) {
	start := 0
	if token != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return nil, "", false
		}
		start, err = strconv.Atoi(string(decoded))
		if err != nil || start < 0 || start > len(items) {
			return nil, "", false
		}
	}
	if limit <= 0 {
		limit = 60
	}
	end := min(start+limit, len(items))
	next := ""
	if end < len(items) {
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return items[start:end], next, true
}

func handleCognitoListUserPools(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	if req.MaxResults < 1 || req.MaxResults > 60 {
		cognitoBadRequest(w, "InvalidParameterException", "1 validation error detected: Value at 'maxResults' failed to satisfy constraint: Member must have value between 1 and 60")
		return
	}
	pools := cognitoPools.List()
	sort.Slice(pools, func(i, j int) bool { return pools[i].Id < pools[j].Id })
	page, next, ok := cognitoPage(pools, req.NextToken, req.MaxResults)
	if !ok {
		cognitoBadRequest(w, "InvalidParameterException", "Invalid pagination token.")
		return
	}
	out := make([]map[string]any, 0, len(page))
	for _, pool := range page {
		entry := map[string]any{
			"Id": pool.Id, "Name": pool.Name, "Status": "Enabled",
			"CreationDate": cognitoTime(pool.CreationDate), "LastModifiedDate": cognitoTime(pool.LastModifiedDate),
		}
		if lambda, ok := pool.Config["LambdaConfig"]; ok {
			entry["LambdaConfig"] = lambda
		}
		out = append(out, entry)
	}
	resp := map[string]any{"UserPools": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleCognitoGetUserPoolMfaConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolId string `json:"UserPoolId"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, ok := cognitoPool(w, req.UserPoolId)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, cognitoMfaJSON(pool))
}

func cognitoMfaJSON(pool CognitoUserPool) map[string]any {
	out := map[string]any{"MfaConfiguration": pool.MfaConfiguration}
	for k, v := range pool.MfaSettings {
		out[k] = v
	}
	return out
}

func handleCognitoSetUserPoolMfaConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolId       string `json:"UserPoolId"`
		MfaConfiguration string `json:"MfaConfiguration"`
	}
	raw, ok := cognitoDecodeRaw(w, r, &req)
	if !ok {
		return
	}
	pool, ok := cognitoPool(w, req.UserPoolId)
	if !ok {
		return
	}
	switch req.MfaConfiguration {
	case "", "OFF", "ON", "OPTIONAL":
	default:
		cognitoBadRequest(w, "InvalidParameterException", "Invalid MFA configuration given.")
		return
	}
	if req.MfaConfiguration != "" {
		pool.MfaConfiguration = req.MfaConfiguration
	}
	for _, member := range []string{"SmsMfaConfiguration", "SoftwareTokenMfaConfiguration", "EmailMfaConfiguration", "WebAuthnConfiguration"} {
		if v, ok := raw[member]; ok {
			pool.MfaSettings[member] = v
		}
	}
	pool.LastModifiedDate = time.Now().UTC()
	cognitoPools.Put(pool.Id, pool)
	sim.WriteJSON(w, http.StatusOK, cognitoMfaJSON(pool))
}

type cognitoClientRequest struct {
	UserPoolId                               string                     `json:"UserPoolId"`
	ClientId                                 string                     `json:"ClientId"`
	ClientName                               string                     `json:"ClientName"`
	GenerateSecret                           bool                       `json:"GenerateSecret"`
	RefreshTokenValidity                     int                        `json:"RefreshTokenValidity"`
	AccessTokenValidity                      int                        `json:"AccessTokenValidity"`
	IdTokenValidity                          int                        `json:"IdTokenValidity"`
	TokenValidityUnits                       *CognitoTokenValidityUnits `json:"TokenValidityUnits"`
	ReadAttributes                           []string                   `json:"ReadAttributes"`
	WriteAttributes                          []string                   `json:"WriteAttributes"`
	ExplicitAuthFlows                        []string                   `json:"ExplicitAuthFlows"`
	SupportedIdentityProviders               []string                   `json:"SupportedIdentityProviders"`
	CallbackURLs                             []string                   `json:"CallbackURLs"`
	LogoutURLs                               []string                   `json:"LogoutURLs"`
	DefaultRedirectURI                       string                     `json:"DefaultRedirectURI"`
	AllowedOAuthFlows                        []string                   `json:"AllowedOAuthFlows"`
	AllowedOAuthScopes                       []string                   `json:"AllowedOAuthScopes"`
	AllowedOAuthFlowsUserPoolClient          bool                       `json:"AllowedOAuthFlowsUserPoolClient"`
	PreventUserExistenceErrors               string                     `json:"PreventUserExistenceErrors"`
	EnableTokenRevocation                    *bool                      `json:"EnableTokenRevocation"`
	EnablePropagateAdditionalUserContextData bool                       `json:"EnablePropagateAdditionalUserContextData"`
	AuthSessionValidity                      int                        `json:"AuthSessionValidity"`
}

var cognitoClientMembers = []string{
	"UserPoolId", "ClientId", "ClientName", "GenerateSecret", "RefreshTokenValidity", "AccessTokenValidity",
	"IdTokenValidity", "TokenValidityUnits", "ReadAttributes", "WriteAttributes", "ExplicitAuthFlows",
	"SupportedIdentityProviders", "CallbackURLs", "LogoutURLs", "DefaultRedirectURI", "AllowedOAuthFlows",
	"AllowedOAuthScopes", "AllowedOAuthFlowsUserPoolClient", "PreventUserExistenceErrors", "EnableTokenRevocation",
	"EnablePropagateAdditionalUserContextData", "AuthSessionValidity",
}

var cognitoScopePattern = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]+$`)

// cognitoApplyClientRequest sets every member a CreateUserPoolClient or
// UpdateUserPoolClient request carries; UpdateUserPoolClient resets a member
// it omits to its default.
func cognitoApplyClientRequest(client *CognitoUserPoolClient, req cognitoClientRequest, raw map[string]json.RawMessage) string {
	if req.ClientName == "" || !cognitoPoolNamePattern.MatchString(req.ClientName) {
		return "1 validation error detected: Value at 'clientName' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\w\\s+=,.@-]+"
	}
	units := CognitoTokenValidityUnits{}
	if req.TokenValidityUnits != nil {
		units = *req.TokenValidityUnits
	}
	for _, unit := range []string{units.AccessToken, units.IdToken, units.RefreshToken} {
		switch unit {
		case "", "seconds", "minutes", "hours", "days":
		default:
			return "Invalid time unit " + unit + "."
		}
	}
	refresh := req.RefreshTokenValidity
	if refresh == 0 {
		refresh, units.RefreshToken = 30, cmpOr(units.RefreshToken, "days")
	}
	if s := cognitoSeconds(refresh, units.RefreshToken, "days"); s < 60 || s > 315360000 {
		return "Invalid range for token validity."
	}
	for _, v := range []struct {
		value int
		unit  string
	}{{req.AccessTokenValidity, units.AccessToken}, {req.IdTokenValidity, units.IdToken}} {
		if v.value == 0 {
			continue
		}
		if s := cognitoSeconds(v.value, v.unit, "hours"); s < 300 || s > 86400 {
			return "Invalid range for token validity."
		}
	}
	flows := req.ExplicitAuthFlows
	if len(flows) == 0 {
		flows = []string{"ALLOW_REFRESH_TOKEN_AUTH", "ALLOW_USER_SRP_AUTH", "ALLOW_CUSTOM_AUTH"}
	}
	for _, flow := range req.AllowedOAuthFlows {
		if flow != "code" && flow != "implicit" && flow != "client_credentials" {
			return "Invalid OAuth flow " + flow + "."
		}
	}
	if len(req.AllowedOAuthFlows) > 0 && !req.AllowedOAuthFlowsUserPoolClient {
		return "AllowedOAuthFlowsUserPoolClient must be true to allow OAuth flows."
	}
	if slices.Contains(req.AllowedOAuthFlows, "client_credentials") && (slices.Contains(req.AllowedOAuthFlows, "code") || slices.Contains(req.AllowedOAuthFlows, "implicit")) {
		return "client_credentials flow can not be selected along with code flow or implicit flow."
	}
	if (slices.Contains(req.AllowedOAuthFlows, "code") || slices.Contains(req.AllowedOAuthFlows, "implicit")) && len(req.CallbackURLs) == 0 {
		return "CallbackURLs must be provided for code and implicit flows."
	}
	for _, scope := range req.AllowedOAuthScopes {
		if !cognitoScopePattern.MatchString(scope) {
			return "Invalid scope requested: " + scope
		}
	}
	for _, u := range append(slices.Clone(req.CallbackURLs), req.LogoutURLs...) {
		if !cognitoRedirectAllowed(u) {
			return "Invalid redirect URL " + u + "."
		}
	}
	if req.DefaultRedirectURI != "" && !slices.Contains(req.CallbackURLs, req.DefaultRedirectURI) {
		return "DefaultRedirectURI must be one of the CallbackURLs."
	}
	session := req.AuthSessionValidity
	if session == 0 {
		session = 3
	}
	if session < 3 || session > 15 {
		return "1 validation error detected: Value at 'authSessionValidity' failed to satisfy constraint: Member must have value between 3 and 15"
	}
	client.ClientName = req.ClientName
	client.RefreshTokenValidity = refresh
	client.AccessTokenValidity = req.AccessTokenValidity
	client.IdTokenValidity = req.IdTokenValidity
	client.TokenValidityUnits = units
	client.ReadAttributes = req.ReadAttributes
	client.WriteAttributes = req.WriteAttributes
	client.ExplicitAuthFlows = flows
	client.SupportedIdentityProviders = req.SupportedIdentityProviders
	client.CallbackURLs = req.CallbackURLs
	client.LogoutURLs = req.LogoutURLs
	client.DefaultRedirectURI = req.DefaultRedirectURI
	client.AllowedOAuthFlows = req.AllowedOAuthFlows
	client.AllowedOAuthScopes = req.AllowedOAuthScopes
	client.AllowedOAuthFlowsUserPoolClient = req.AllowedOAuthFlowsUserPoolClient
	client.PreventUserExistenceErrors = cmpOr(req.PreventUserExistenceErrors, "LEGACY")
	client.EnableTokenRevocation = req.EnableTokenRevocation == nil || *req.EnableTokenRevocation
	client.EnablePropagateUserContextData = req.EnablePropagateAdditionalUserContextData
	client.AuthSessionValidity = session
	client.Config = map[string]json.RawMessage{}
	for k, v := range raw {
		if !slices.Contains(cognitoClientMembers, k) {
			client.Config[k] = v
		}
	}
	return ""
}

func cmpOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// cognitoRedirectAllowed applies the rule the authorize endpoint states: an
// absolute URI without a fragment, over HTTPS except for http://localhost; an
// app scheme such as myapp://example is allowed.
func cognitoRedirectAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Fragment != "" {
		return false
	}
	if u.Scheme == "http" {
		return u.Hostname() == "localhost"
	}
	return true
}

func cognitoSeconds(value int, unit, fallback string) int {
	switch cmpOr(unit, fallback) {
	case "seconds":
		return value
	case "minutes":
		return value * 60
	case "hours":
		return value * 3600
	default:
		return value * 86400
	}
}

func cognitoClientJSON(client CognitoUserPoolClient) map[string]any {
	out := map[string]any{}
	for k, v := range client.Config {
		out[k] = v
	}
	out["UserPoolId"] = client.UserPoolId
	out["ClientId"] = client.ClientId
	out["ClientName"] = client.ClientName
	out["CreationDate"] = cognitoTime(client.CreationDate)
	out["LastModifiedDate"] = cognitoTime(client.LastModifiedDate)
	out["RefreshTokenValidity"] = client.RefreshTokenValidity
	out["ExplicitAuthFlows"] = client.ExplicitAuthFlows
	out["AllowedOAuthFlowsUserPoolClient"] = client.AllowedOAuthFlowsUserPoolClient
	out["PreventUserExistenceErrors"] = client.PreventUserExistenceErrors
	out["EnableTokenRevocation"] = client.EnableTokenRevocation
	out["EnablePropagateAdditionalUserContextData"] = client.EnablePropagateUserContextData
	out["AuthSessionValidity"] = client.AuthSessionValidity
	if client.TokenValidityUnits != (CognitoTokenValidityUnits{}) {
		out["TokenValidityUnits"] = client.TokenValidityUnits
	}
	for k, v := range map[string]int{"AccessTokenValidity": client.AccessTokenValidity, "IdTokenValidity": client.IdTokenValidity} {
		if v != 0 {
			out[k] = v
		}
	}
	for k, v := range map[string]string{"ClientSecret": client.ClientSecret, "DefaultRedirectURI": client.DefaultRedirectURI} {
		if v != "" {
			out[k] = v
		}
	}
	for k, v := range map[string][]string{
		"ReadAttributes": client.ReadAttributes, "WriteAttributes": client.WriteAttributes,
		"SupportedIdentityProviders": client.SupportedIdentityProviders, "CallbackURLs": client.CallbackURLs,
		"LogoutURLs": client.LogoutURLs, "AllowedOAuthFlows": client.AllowedOAuthFlows, "AllowedOAuthScopes": client.AllowedOAuthScopes,
	} {
		if len(v) > 0 {
			out[k] = v
		}
	}
	return out
}

func cognitoClientFor(w http.ResponseWriter, poolID, clientID string) (CognitoUserPoolClient, bool) {
	if _, ok := cognitoPool(w, poolID); !ok {
		return CognitoUserPoolClient{}, false
	}
	client, ok := cognitoClients.Get(poolID + "/" + clientID)
	if !ok {
		cognitoBadRequest(w, "ResourceNotFoundException", "User pool client "+clientID+" does not exist.")
	}
	return client, ok
}

func handleCognitoCreateUserPoolClient(w http.ResponseWriter, r *http.Request) {
	var req cognitoClientRequest
	raw, ok := cognitoDecodeRaw(w, r, &req)
	if !ok {
		return
	}
	if _, ok := cognitoPool(w, req.UserPoolId); !ok {
		return
	}
	now := time.Now().UTC()
	client := CognitoUserPoolClient{
		UserPoolId:       req.UserPoolId,
		ClientId:         cognitoRandom(cognitoLowerAlnum, 26),
		CreationDate:     now,
		LastModifiedDate: now,
	}
	if req.GenerateSecret {
		client.ClientSecret = cognitoRandom(cognitoLowerAlnum, 51)
	}
	if msg := cognitoApplyClientRequest(&client, req, raw); msg != "" {
		cognitoBadRequest(w, "InvalidParameterException", msg)
		return
	}
	if slices.Contains(client.AllowedOAuthFlows, "client_credentials") && client.ClientSecret == "" {
		cognitoBadRequest(w, "InvalidOAuthFlowException", "client_credentials flow can not be selected if client does not have a client secret.")
		return
	}
	cognitoClients.Put(client.UserPoolId+"/"+client.ClientId, client)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"UserPoolClient": cognitoClientJSON(client)})
}

func handleCognitoDescribeUserPoolClient(w http.ResponseWriter, r *http.Request) {
	var req cognitoClientRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	client, ok := cognitoClientFor(w, req.UserPoolId, req.ClientId)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"UserPoolClient": cognitoClientJSON(client)})
}

func handleCognitoUpdateUserPoolClient(w http.ResponseWriter, r *http.Request) {
	var req cognitoClientRequest
	raw, ok := cognitoDecodeRaw(w, r, &req)
	if !ok {
		return
	}
	client, ok := cognitoClientFor(w, req.UserPoolId, req.ClientId)
	if !ok {
		return
	}
	if req.ClientName == "" {
		req.ClientName = client.ClientName
	}
	if msg := cognitoApplyClientRequest(&client, req, raw); msg != "" {
		cognitoBadRequest(w, "InvalidParameterException", msg)
		return
	}
	if slices.Contains(client.AllowedOAuthFlows, "client_credentials") && client.ClientSecret == "" {
		cognitoBadRequest(w, "InvalidOAuthFlowException", "client_credentials flow can not be selected if client does not have a client secret.")
		return
	}
	client.LastModifiedDate = time.Now().UTC()
	cognitoClients.Put(client.UserPoolId+"/"+client.ClientId, client)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"UserPoolClient": cognitoClientJSON(client)})
}

func handleCognitoDeleteUserPoolClient(w http.ResponseWriter, r *http.Request) {
	var req cognitoClientRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	client, ok := cognitoClientFor(w, req.UserPoolId, req.ClientId)
	if !ok {
		return
	}
	cognitoClients.Delete(client.UserPoolId + "/" + client.ClientId)
	cognitoForgetClient(client.UserPoolId, client.ClientId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCognitoListUserPoolClients(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolId string `json:"UserPoolId"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	if _, ok := cognitoPool(w, req.UserPoolId); !ok {
		return
	}
	var clients []CognitoUserPoolClient
	for _, kv := range cognitoClients.ListPrefix(req.UserPoolId + "/") {
		clients = append(clients, kv.Item)
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].ClientId < clients[j].ClientId })
	page, next, ok := cognitoPage(clients, req.NextToken, req.MaxResults)
	if !ok {
		cognitoBadRequest(w, "InvalidParameterException", "Invalid pagination token.")
		return
	}
	out := make([]map[string]string, 0, len(page))
	for _, c := range page {
		out = append(out, map[string]string{"ClientId": c.ClientId, "UserPoolId": c.UserPoolId, "ClientName": c.ClientName})
	}
	resp := map[string]any{"UserPoolClients": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

var cognitoDomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?$`)

// The prefix domain the user pool domain guide names:
// <prefix>.auth.<region>.amazoncognito.com.
func cognitoPrefixDomainHost(prefix string) string {
	return prefix + ".auth." + awsRegion() + ".amazoncognito.com"
}

func handleCognitoCreateUserPoolDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain              string          `json:"Domain"`
		UserPoolId          string          `json:"UserPoolId"`
		ManagedLoginVersion int             `json:"ManagedLoginVersion"`
		CustomDomainConfig  json.RawMessage `json:"CustomDomainConfig"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	if len(req.CustomDomainConfig) > 0 || strings.Contains(req.Domain, ".") {
		cognitoBadRequest(w, "InvalidParameterException", "Custom domains are not supported by this user pool domain implementation; use a prefix domain.")
		return
	}
	if !cognitoDomainPattern.MatchString(req.Domain) {
		cognitoBadRequest(w, "InvalidParameterException", "1 validation error detected: Value at 'domain' failed to satisfy constraint: Member must satisfy regular expression pattern: ^[a-z0-9](?:[a-z0-9\\-]{0,61}[a-z0-9])?$")
		return
	}
	for _, reserved := range []string{"aws", "amazon", "cognito"} {
		if strings.Contains(req.Domain, reserved) {
			cognitoBadRequest(w, "InvalidParameterException", "Domain cannot contain reserved word: "+reserved)
			return
		}
	}
	pool, ok := cognitoPool(w, req.UserPoolId)
	if !ok {
		return
	}
	if pool.Domain != "" {
		cognitoBadRequest(w, "InvalidParameterException", "User pool already has a domain configured.")
		return
	}
	if _, taken := cognitoDomains.Get(req.Domain); taken {
		cognitoBadRequest(w, "InvalidParameterException", "Domain already associated with another user pool.")
		return
	}
	version := 1
	if req.ManagedLoginVersion != 0 {
		version = req.ManagedLoginVersion
	}
	cognitoDomains.Put(req.Domain, CognitoDomain{Domain: req.Domain, UserPoolId: pool.Id, Version: strconv.Itoa(version)})
	cognitoPools.Update(pool.Id, func(p *CognitoUserPool) { p.Domain = req.Domain })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ManagedLoginVersion": version})
}

func handleCognitoDescribeUserPoolDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"Domain"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	domain, ok := cognitoDomains.Get(req.Domain)
	if !ok {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"DomainDescription": map[string]any{}})
		return
	}
	version, _ := strconv.Atoi(domain.Version)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"DomainDescription": map[string]any{
		"UserPoolId":          domain.UserPoolId,
		"AWSAccountId":        awsAccountID(),
		"Domain":              domain.Domain,
		"Status":              "ACTIVE",
		"Version":             domain.Version,
		"ManagedLoginVersion": version,
	}})
}

func handleCognitoDeleteUserPoolDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain     string `json:"Domain"`
		UserPoolId string `json:"UserPoolId"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	if _, ok := cognitoPool(w, req.UserPoolId); !ok {
		return
	}
	domain, ok := cognitoDomains.Get(req.Domain)
	if !ok || domain.UserPoolId != req.UserPoolId {
		cognitoBadRequest(w, "InvalidParameterException", "No such domain or user pool exists.")
		return
	}
	cognitoDomains.Delete(req.Domain)
	cognitoPools.Update(req.UserPoolId, func(p *CognitoUserPool) { p.Domain = "" })
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func cognitoUserKey(pool CognitoUserPool, username string) string {
	if !pool.CaseSensitive {
		username = strings.ToLower(username)
	}
	return pool.Id + "/" + username
}

func cognitoUserFor(w http.ResponseWriter, poolID, username string) (CognitoUserPool, CognitoUser, bool) {
	pool, ok := cognitoPool(w, poolID)
	if !ok {
		return pool, CognitoUser{}, false
	}
	user, ok := cognitoUsers.Get(cognitoUserKey(pool, username))
	if !ok {
		cognitoBadRequest(w, "UserNotFoundException", "User does not exist.")
	}
	return pool, user, ok
}

// cognitoPasswordViolation checks a password against the pool's policy and
// returns the requirement it misses.
func cognitoPasswordViolation(policy CognitoPasswordPolicy, password string) string {
	if len(password) < policy.MinimumLength {
		return "Password not long enough"
	}
	var upper, lower, number, symbol bool
	for _, c := range password {
		switch {
		case unicode.IsUpper(c):
			upper = true
		case unicode.IsLower(c):
			lower = true
		case unicode.IsDigit(c):
			number = true
		case !unicode.IsSpace(c):
			symbol = true
		}
	}
	switch {
	case policy.RequireUppercase && !upper:
		return "Password must have uppercase characters"
	case policy.RequireLowercase && !lower:
		return "Password must have lowercase characters"
	case policy.RequireNumbers && !number:
		return "Password must have numeric characters"
	case policy.RequireSymbols && !symbol:
		return "Password must have symbol characters"
	}
	return ""
}

func cognitoSetPassword(user *CognitoUser, password string) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := cognitoHashPassword(password, salt)
	if err != nil {
		return err
	}
	user.PasswordSalt, user.PasswordHash = salt, hash
	return nil
}

// cognitoTemporaryPassword draws a password that satisfies the pool's policy.
func cognitoTemporaryPassword(policy CognitoPasswordPolicy) string {
	length := max(policy.MinimumLength, 12)
	return cognitoRandom("ABCDEFGHJKLMNPQRSTUVWXYZ", 2) + cognitoRandom("abcdefghijkmnopqrstuvwxyz", 2) +
		cognitoRandom("23456789", 2) + cognitoRandom("!#$%&*+-=?^_", 2) + cognitoRandom(cognitoAlnum, length-8)
}

func cognitoUserAttributes(attrs []CognitoAttribute, name string) (string, bool) {
	for _, a := range attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

func handleCognitoAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolId        string             `json:"UserPoolId"`
		Username          string             `json:"Username"`
		UserAttributes    []CognitoAttribute `json:"UserAttributes"`
		TemporaryPassword string             `json:"TemporaryPassword"`
		MessageAction     string             `json:"MessageAction"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, ok := cognitoPool(w, req.UserPoolId)
	if !ok {
		return
	}
	if req.Username == "" || len(req.Username) > 128 {
		cognitoBadRequest(w, "InvalidParameterException", "1 validation error detected: Value at 'username' failed to satisfy constraint: Member must have length between 1 and 128")
		return
	}
	key := cognitoUserKey(pool, req.Username)
	existing, exists := cognitoUsers.Get(key)
	switch req.MessageAction {
	case "", "SUPPRESS":
		if exists {
			cognitoBadRequest(w, "UsernameExistsException", "User account already exists")
			return
		}
	case "RESEND":
		if !exists {
			cognitoBadRequest(w, "UserNotFoundException", "User does not exist.")
			return
		}
	default:
		cognitoBadRequest(w, "InvalidParameterException", "Invalid message action given.")
		return
	}
	for _, a := range req.UserAttributes {
		if a.Name == "sub" {
			cognitoBadRequest(w, "InvalidParameterException", "Cannot modify the non-mutable attribute sub")
			return
		}
		if !cognitoStandardAttributes[a.Name] && !cognitoPoolHasCustom(pool, a.Name) {
			cognitoBadRequest(w, "InvalidParameterException", "Attributes did not conform to the schema: "+a.Name+": Attribute does not exist in the schema.")
			return
		}
	}
	password := req.TemporaryPassword
	if password == "" {
		password = cognitoTemporaryPassword(pool.PasswordPolicy)
	} else if msg := cognitoPasswordViolation(pool.PasswordPolicy, password); msg != "" {
		cognitoBadRequest(w, "InvalidPasswordException", "Password did not conform with policy: "+msg)
		return
	}
	now := time.Now().UTC()
	user := existing
	if !exists {
		user = CognitoUser{
			UserPoolId: pool.Id,
			Username:   req.Username,
			Attributes: append([]CognitoAttribute{{Name: "sub", Value: uuid.NewString()}}, req.UserAttributes...),
			Created:    now,
			Enabled:    true,
		}
	}
	user.Status = "FORCE_CHANGE_PASSWORD"
	user.LastModified = now
	user.TemporaryExpires = now.AddDate(0, 0, pool.PasswordPolicy.TemporaryPasswordValidityDays)
	if err := cognitoSetPassword(&user, password); err != nil {
		AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
		return
	}
	cognitoUsers.Put(key, user)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"User": cognitoUserJSON(user, "Attributes")})
}

func cognitoPoolHasCustom(pool CognitoUserPool, name string) bool {
	for _, attr := range cognitoSchemaJSON(pool.Schema) {
		if attr["Name"] == name {
			return true
		}
	}
	return false
}

func cognitoUserJSON(user CognitoUser, attributesMember string) map[string]any {
	return map[string]any{
		"Username":             user.Username,
		attributesMember:       user.Attributes,
		"UserCreateDate":       cognitoTime(user.Created),
		"UserLastModifiedDate": cognitoTime(user.LastModified),
		"Enabled":              user.Enabled,
		"UserStatus":           user.Status,
	}
}

type cognitoUserRequest struct {
	UserPoolId string `json:"UserPoolId"`
	Username   string `json:"Username"`
	Password   string `json:"Password"`
	Permanent  bool   `json:"Permanent"`
}

func handleCognitoAdminGetUser(w http.ResponseWriter, r *http.Request) {
	var req cognitoUserRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	_, user, ok := cognitoUserFor(w, req.UserPoolId, req.Username)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, cognitoUserJSON(user, "UserAttributes"))
}

func handleCognitoAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	var req cognitoUserRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, user, ok := cognitoUserFor(w, req.UserPoolId, req.Username)
	if !ok {
		return
	}
	cognitoUsers.Delete(cognitoUserKey(pool, user.Username))
	cognitoForgetUser(pool.Id, user.Username)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCognitoAdminSetUserPassword(w http.ResponseWriter, r *http.Request) {
	var req cognitoUserRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, user, ok := cognitoUserFor(w, req.UserPoolId, req.Username)
	if !ok {
		return
	}
	if msg := cognitoPasswordViolation(pool.PasswordPolicy, req.Password); msg != "" {
		cognitoBadRequest(w, "InvalidPasswordException", "Password does not conform to policy: "+msg)
		return
	}
	if err := cognitoSetPassword(&user, req.Password); err != nil {
		AWSError(w, "InternalErrorException", err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	user.LastModified = now
	if req.Permanent {
		user.Status = "CONFIRMED"
		user.TemporaryExpires = time.Time{}
	} else {
		user.Status = "FORCE_CHANGE_PASSWORD"
		user.TemporaryExpires = now.AddDate(0, 0, pool.PasswordPolicy.TemporaryPasswordValidityDays)
	}
	cognitoUsers.Put(cognitoUserKey(pool, user.Username), user)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func cognitoSetEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	var req cognitoUserRequest
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, user, ok := cognitoUserFor(w, req.UserPoolId, req.Username)
	if !ok {
		return
	}
	user.Enabled = enabled
	user.LastModified = time.Now().UTC()
	cognitoUsers.Put(cognitoUserKey(pool, user.Username), user)
	if !enabled {
		cognitoForgetUser(pool.Id, user.Username)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCognitoAdminEnableUser(w http.ResponseWriter, r *http.Request) {
	cognitoSetEnabled(w, r, true)
}

func handleCognitoAdminDisableUser(w http.ResponseWriter, r *http.Request) {
	cognitoSetEnabled(w, r, false)
}

// The ListUsers filter grammar: an attribute, = for an exact match or ^= for a
// prefix, and a quoted value.
var cognitoFilterPattern = regexp.MustCompile(`^\s*([\w:]+)\s*(\^?=)\s*"(.*)"\s*$`)

var cognitoFilterAttributes = map[string]bool{
	"username": true, "email": true, "phone_number": true, "name": true, "given_name": true, "family_name": true,
	"preferred_username": true, "cognito:user_status": true, "status": true, "sub": true,
}

func handleCognitoListUsers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolId      string   `json:"UserPoolId"`
		AttributesToGet []string `json:"AttributesToGet"`
		Limit           int      `json:"Limit"`
		PaginationToken string   `json:"PaginationToken"`
		Filter          string   `json:"Filter"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	if _, ok := cognitoPool(w, req.UserPoolId); !ok {
		return
	}
	match := func(CognitoUser) bool { return true }
	if req.Filter != "" {
		m := cognitoFilterPattern.FindStringSubmatch(req.Filter)
		if m == nil || !cognitoFilterAttributes[m[1]] {
			cognitoBadRequest(w, "InvalidParameterException", "Error while parsing filter.")
			return
		}
		attr, prefix, value := m[1], m[2] == "^=", m[3]
		match = func(u CognitoUser) bool {
			var got string
			switch attr {
			case "username":
				got = u.Username
			case "cognito:user_status":
				got = u.Status
			case "status":
				got = map[bool]string{true: "Enabled", false: "Disabled"}[u.Enabled]
			default:
				got, _ = cognitoUserAttributes(u.Attributes, attr)
			}
			if prefix {
				return strings.HasPrefix(got, value)
			}
			return got == value
		}
	}
	var users []CognitoUser
	for _, kv := range cognitoUsers.ListPrefix(req.UserPoolId + "/") {
		if match(kv.Item) {
			users = append(users, kv.Item)
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	page, next, ok := cognitoPage(users, req.PaginationToken, req.Limit)
	if !ok {
		cognitoBadRequest(w, "InvalidParameterException", "Invalid pagination token.")
		return
	}
	out := make([]map[string]any, 0, len(page))
	for _, u := range page {
		if len(req.AttributesToGet) > 0 {
			var kept []CognitoAttribute
			for _, a := range u.Attributes {
				if slices.Contains(req.AttributesToGet, a.Name) {
					kept = append(kept, a)
				}
			}
			u.Attributes = kept
		}
		out = append(out, cognitoUserJSON(u, "Attributes"))
	}
	resp := map[string]any{"Users": out}
	if next != "" {
		resp["PaginationToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// cognitoPoolForARN reads the user pool a ResourceArn names.
func cognitoPoolForARN(w http.ResponseWriter, arn string) (CognitoUserPool, bool) {
	prefix := fmt.Sprintf("arn:aws:cognito-idp:%s:%s:userpool/", awsRegion(), awsAccountID())
	if !strings.HasPrefix(arn, prefix) {
		cognitoBadRequest(w, "InvalidParameterException", "Invalid resource ARN "+arn+".")
		return CognitoUserPool{}, false
	}
	return cognitoPool(w, strings.TrimPrefix(arn, prefix))
}

func handleCognitoTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string            `json:"ResourceArn"`
		Tags        map[string]string `json:"Tags"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, ok := cognitoPoolForARN(w, req.ResourceArn)
	if !ok {
		return
	}
	cognitoPools.Update(pool.Id, func(p *CognitoUserPool) {
		if p.Tags == nil {
			p.Tags = map[string]string{}
		}
		for k, v := range req.Tags {
			p.Tags[k] = v
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCognitoUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"ResourceArn"`
		TagKeys     []string `json:"TagKeys"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, ok := cognitoPoolForARN(w, req.ResourceArn)
	if !ok {
		return
	}
	cognitoPools.Update(pool.Id, func(p *CognitoUserPool) {
		for _, k := range req.TagKeys {
			delete(p.Tags, k)
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCognitoListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if !cognitoReadRequest(w, r, &req) {
		return
	}
	pool, ok := cognitoPoolForARN(w, req.ResourceArn)
	if !ok {
		return
	}
	tags := pool.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}
