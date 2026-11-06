package aws_sdk_test

import (
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ciptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cognitoClient() *cip.Client {
	return cip.NewFromConfig(sdkConfig(), func(o *cip.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestCognito_UserPoolLifecycle covers a user pool's configuration, app
// clients, prefix domain, users and tags through the control plane.
func TestCognito_UserPoolLifecycle(t *testing.T) {
	t.Parallel()
	c := cognitoClient()
	created, err := c.CreateUserPool(ctx, &cip.CreateUserPoolInput{
		PoolName: aws.String(uniquename.New("pool")),
		Policies: &ciptypes.UserPoolPolicyType{PasswordPolicy: &ciptypes.PasswordPolicyType{
			MinimumLength: aws.Int32(10), RequireNumbers: true, RequireUppercase: true,
		}},
		Schema: []ciptypes.SchemaAttributeType{{
			Name: aws.String("team"), AttributeDataType: ciptypes.AttributeDataTypeString, Mutable: aws.Bool(true),
		}},
		UsernameConfiguration: &ciptypes.UsernameConfigurationType{CaseSensitive: aws.Bool(false)},
		UserPoolTags:          map[string]string{"env": "test"},
		DeletionProtection:    ciptypes.DeletionProtectionTypeActive,
	})
	require.NoError(t, err)
	pool := created.UserPool
	poolID := aws.ToString(pool.Id)
	assert.True(t, strings.HasPrefix(poolID, "us-east-1_"), poolID)
	assert.Equal(t, "arn:aws:cognito-idp:us-east-1:123456789012:userpool/"+poolID, aws.ToString(pool.Arn))
	assert.Equal(t, int32(7), pool.Policies.PasswordPolicy.TemporaryPasswordValidityDays, "a temporary password is valid for 7 days by default")
	assert.Equal(t, ciptypes.UserPoolMfaTypeOff, pool.MfaConfiguration)
	require.Len(t, pool.SchemaAttributes, 1)
	assert.Equal(t, "custom:team", aws.ToString(pool.SchemaAttributes[0].Name))

	_, err = c.DeleteUserPool(ctx, &cip.DeleteUserPoolInput{UserPoolId: aws.String(poolID)})
	var invalid *ciptypes.InvalidParameterException
	require.ErrorAs(t, err, &invalid, "deletion protection refuses the delete")

	client, err := c.CreateUserPoolClient(ctx, &cip.CreateUserPoolClientInput{
		UserPoolId: aws.String(poolID), ClientName: aws.String("web"), GenerateSecret: true,
		AllowedOAuthFlows: []ciptypes.OAuthFlowType{ciptypes.OAuthFlowTypeCode}, AllowedOAuthFlowsUserPoolClient: true,
		AllowedOAuthScopes: []string{"openid", "email"}, CallbackURLs: []string{"https://app.example.com/callback"},
		SupportedIdentityProviders: []string{"COGNITO"},
	})
	require.NoError(t, err)
	clientID := aws.ToString(client.UserPoolClient.ClientId)
	assert.NotEmpty(t, aws.ToString(client.UserPoolClient.ClientSecret))
	assert.Equal(t, int32(30), client.UserPoolClient.RefreshTokenValidity, "refresh tokens are valid for 30 days by default")
	assert.ElementsMatch(t, []ciptypes.ExplicitAuthFlowsType{"ALLOW_REFRESH_TOKEN_AUTH", "ALLOW_USER_SRP_AUTH", "ALLOW_CUSTOM_AUTH"},
		client.UserPoolClient.ExplicitAuthFlows)

	_, err = c.CreateUserPoolClient(ctx, &cip.CreateUserPoolClientInput{
		UserPoolId: aws.String(poolID), ClientName: aws.String("plain-http"),
		AllowedOAuthFlows: []ciptypes.OAuthFlowType{ciptypes.OAuthFlowTypeCode}, AllowedOAuthFlowsUserPoolClient: true,
		AllowedOAuthScopes: []string{"openid"}, CallbackURLs: []string{"http://app.example.com/callback"},
	})
	require.ErrorAs(t, err, &invalid, "a callback URL uses HTTPS unless it is http://localhost")

	updated, err := c.UpdateUserPoolClient(ctx, &cip.UpdateUserPoolClientInput{
		UserPoolId: aws.String(poolID), ClientId: aws.String(clientID), ClientName: aws.String("web-renamed"),
		AccessTokenValidity: aws.Int32(30), TokenValidityUnits: &ciptypes.TokenValidityUnitsType{AccessToken: ciptypes.TimeUnitsTypeMinutes},
	})
	require.NoError(t, err)
	assert.Equal(t, "web-renamed", aws.ToString(updated.UserPoolClient.ClientName))
	assert.Empty(t, updated.UserPoolClient.CallbackURLs, "an update resets a member it omits")
	readBack, err := c.DescribeUserPoolClient(ctx, &cip.DescribeUserPoolClientInput{UserPoolId: aws.String(poolID), ClientId: aws.String(clientID)})
	require.NoError(t, err)
	assert.Equal(t, int32(30), aws.ToInt32(readBack.UserPoolClient.AccessTokenValidity))
	assert.Equal(t, ciptypes.TimeUnitsTypeMinutes, readBack.UserPoolClient.TokenValidityUnits.AccessToken)
	assert.Equal(t, aws.ToString(client.UserPoolClient.ClientSecret), aws.ToString(readBack.UserPoolClient.ClientSecret), "an update keeps the secret")
	listed, err := c.ListUserPoolClients(ctx, &cip.ListUserPoolClientsInput{UserPoolId: aws.String(poolID)})
	require.NoError(t, err)
	require.Len(t, listed.UserPoolClients, 1)
	assert.Equal(t, clientID, aws.ToString(listed.UserPoolClients[0].ClientId))

	prefix := strings.ToLower(strings.ReplaceAll(uniquename.New("login"), "_", "-"))
	_, err = c.CreateUserPoolDomain(ctx, &cip.CreateUserPoolDomainInput{UserPoolId: aws.String(poolID), Domain: aws.String(prefix)})
	require.NoError(t, err)
	domain, err := c.DescribeUserPoolDomain(ctx, &cip.DescribeUserPoolDomainInput{Domain: aws.String(prefix)})
	require.NoError(t, err)
	assert.Equal(t, poolID, aws.ToString(domain.DomainDescription.UserPoolId))
	assert.Equal(t, ciptypes.DomainStatusTypeActive, domain.DomainDescription.Status)
	described, err := c.DescribeUserPool(ctx, &cip.DescribeUserPoolInput{UserPoolId: aws.String(poolID)})
	require.NoError(t, err)
	assert.Equal(t, prefix, aws.ToString(described.UserPool.Domain))

	_, err = c.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId: aws.String(poolID), Username: aws.String("Alice"), TemporaryPassword: aws.String("short1A"),
	})
	var weak *ciptypes.InvalidPasswordException
	require.ErrorAs(t, err, &weak, "the temporary password must meet the pool's policy")
	user, err := c.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId: aws.String(poolID), Username: aws.String("Alice"), MessageAction: ciptypes.MessageActionTypeSuppress,
		UserAttributes: []ciptypes.AttributeType{{Name: aws.String("email"), Value: aws.String("alice@example.com")},
			{Name: aws.String("custom:team"), Value: aws.String("blue")}},
	})
	require.NoError(t, err)
	assert.Equal(t, ciptypes.UserStatusTypeForceChangePassword, user.User.UserStatus)
	_, err = c.AdminCreateUser(ctx, &cip.AdminCreateUserInput{UserPoolId: aws.String(poolID), Username: aws.String("alice")})
	var exists *ciptypes.UsernameExistsException
	require.ErrorAs(t, err, &exists, "the pool matches user names case-insensitively")

	_, err = c.AdminSetUserPassword(ctx, &cip.AdminSetUserPasswordInput{
		UserPoolId: aws.String(poolID), Username: aws.String("alice"), Password: aws.String("Correct-Horse-9"), Permanent: true,
	})
	require.NoError(t, err)
	got, err := c.AdminGetUser(ctx, &cip.AdminGetUserInput{UserPoolId: aws.String(poolID), Username: aws.String("ALICE")})
	require.NoError(t, err)
	assert.Equal(t, ciptypes.UserStatusTypeConfirmed, got.UserStatus)
	assert.True(t, got.Enabled)
	_, err = c.AdminDisableUser(ctx, &cip.AdminDisableUserInput{UserPoolId: aws.String(poolID), Username: aws.String("Alice")})
	require.NoError(t, err)
	users, err := c.ListUsers(ctx, &cip.ListUsersInput{UserPoolId: aws.String(poolID), Filter: aws.String(`email ^= "alice@"`)})
	require.NoError(t, err)
	require.Len(t, users.Users, 1)
	assert.False(t, users.Users[0].Enabled)
	_, err = c.AdminEnableUser(ctx, &cip.AdminEnableUserInput{UserPoolId: aws.String(poolID), Username: aws.String("alice")})
	require.NoError(t, err)
	enabled, err := c.AdminGetUser(ctx, &cip.AdminGetUserInput{UserPoolId: aws.String(poolID), Username: aws.String("alice")})
	require.NoError(t, err)
	assert.True(t, enabled.Enabled)
	none, err := c.ListUsers(ctx, &cip.ListUsersInput{UserPoolId: aws.String(poolID), Filter: aws.String(`email = "bob@example.com"`)})
	require.NoError(t, err)
	assert.Empty(t, none.Users)

	mfa, err := c.SetUserPoolMfaConfig(ctx, &cip.SetUserPoolMfaConfigInput{
		UserPoolId: aws.String(poolID), SoftwareTokenMfaConfiguration: &ciptypes.SoftwareTokenMfaConfigType{Enabled: true},
		MfaConfiguration: ciptypes.UserPoolMfaTypeOptional,
	})
	require.NoError(t, err)
	assert.Equal(t, ciptypes.UserPoolMfaTypeOptional, mfa.MfaConfiguration)
	readMfa, err := c.GetUserPoolMfaConfig(ctx, &cip.GetUserPoolMfaConfigInput{UserPoolId: aws.String(poolID)})
	require.NoError(t, err)
	assert.True(t, readMfa.SoftwareTokenMfaConfiguration.Enabled)

	_, err = c.TagResource(ctx, &cip.TagResourceInput{ResourceArn: pool.Arn, Tags: map[string]string{"team": "auth"}})
	require.NoError(t, err)
	_, err = c.UntagResource(ctx, &cip.UntagResourceInput{ResourceArn: pool.Arn, TagKeys: []string{"env"}})
	require.NoError(t, err)
	tags, err := c.ListTagsForResource(ctx, &cip.ListTagsForResourceInput{ResourceArn: pool.Arn})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "auth"}, tags.Tags)

	_, err = c.UpdateUserPool(ctx, &cip.UpdateUserPoolInput{UserPoolId: aws.String(poolID), DeletionProtection: ciptypes.DeletionProtectionTypeInactive})
	require.NoError(t, err)
	_, err = c.DeleteUserPool(ctx, &cip.DeleteUserPoolInput{UserPoolId: aws.String(poolID)})
	require.ErrorAs(t, err, &invalid, "a pool with a domain refuses the delete")
	_, err = c.DeleteUserPoolDomain(ctx, &cip.DeleteUserPoolDomainInput{UserPoolId: aws.String(poolID), Domain: aws.String(prefix)})
	require.NoError(t, err)
	gone, err := c.DescribeUserPoolDomain(ctx, &cip.DescribeUserPoolDomainInput{Domain: aws.String(prefix)})
	require.NoError(t, err)
	assert.Nil(t, gone.DomainDescription.UserPoolId, "a domain no pool holds describes as empty")
	_, err = c.AdminDeleteUser(ctx, &cip.AdminDeleteUserInput{UserPoolId: aws.String(poolID), Username: aws.String("alice")})
	require.NoError(t, err)
	_, err = c.DeleteUserPoolClient(ctx, &cip.DeleteUserPoolClientInput{UserPoolId: aws.String(poolID), ClientId: aws.String(clientID)})
	require.NoError(t, err)
	_, err = c.DeleteUserPool(ctx, &cip.DeleteUserPoolInput{UserPoolId: aws.String(poolID)})
	require.NoError(t, err)
	_, err = c.DescribeUserPool(ctx, &cip.DescribeUserPoolInput{UserPoolId: aws.String(poolID)})
	var notFound *ciptypes.ResourceNotFoundException
	require.ErrorAs(t, err, &notFound)
	pools, err := c.ListUserPools(ctx, &cip.ListUserPoolsInput{MaxResults: aws.Int32(60)})
	require.NoError(t, err)
	for _, p := range pools.UserPools {
		assert.NotEqual(t, poolID, aws.ToString(p.Id))
	}
}

// cognitoBrowser is a browser: it keeps cookies, follows the hosted domain's
// redirects, and stops at the app's callback. It speaks plain HTTP to the
// simulator, which serves the https:// URLs the pool names.
func cognitoBrowser(t *testing.T, appHost string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &http.Client{
		Jar: jar,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			clone := r.Clone(r.Context())
			clone.URL.Scheme = "http"
			return simHTTPClient.GetTransport().RoundTrip(clone)
		}),
		CheckRedirect: func(r *http.Request, _ []*http.Request) error {
			if r.URL.Host == appHost {
				return http.ErrUseLastResponse
			}
			return nil
		},
		Timeout: 30 * time.Second,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func cognitoRead(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// cognitoVerifyJWT checks an RS256 token against the issuer's published keys,
// as a relying party does, and returns its claims.
func cognitoVerifyJWT(t *testing.T, browser *http.Client, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	var header struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &header))
	require.Equal(t, "RS256", header.Alg)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))

	resp, err := browser.Get(claims["iss"].(string) + "/.well-known/jwks.json")
	require.NoError(t, err)
	var jwks struct {
		Keys []struct{ Kid, N, E string } `json:"keys"`
	}
	require.NoError(t, json.Unmarshal([]byte(cognitoRead(t, resp)), &jwks))
	var key *rsa.PublicKey
	for _, k := range jwks.Keys {
		if k.Kid == header.Kid {
			n, err := base64.RawURLEncoding.DecodeString(k.N)
			require.NoError(t, err)
			e, err := base64.RawURLEncoding.DecodeString(k.E)
			require.NoError(t, err)
			key = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		}
	}
	require.NotNil(t, key, "the issuer publishes the token's key %s", header.Kid)
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	require.NoError(t, rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature))
	return claims
}

// TestCognito_HostedDomainAuthorizationCodeFlow signs a user in through the
// pool's prefix domain the way a browser does, redeems the code with PKCE at
// the token endpoint, checks the ID token against the issuer's keys, reads the
// user through userInfo, signs in again on the session cookie, refreshes, and
// signs out.
func TestCognito_HostedDomainAuthorizationCodeFlow(t *testing.T) {
	t.Parallel()
	c := cognitoClient()
	created, err := c.CreateUserPool(ctx, &cip.CreateUserPoolInput{PoolName: aws.String(uniquename.New("hosted"))})
	require.NoError(t, err)
	poolID := aws.ToString(created.UserPool.Id)
	const callback, signedOut = "https://app.example.com/callback", "https://app.example.com/bye"
	client, err := c.CreateUserPoolClient(ctx, &cip.CreateUserPoolClientInput{
		UserPoolId: aws.String(poolID), ClientName: aws.String("web"), GenerateSecret: true,
		AllowedOAuthFlows: []ciptypes.OAuthFlowType{ciptypes.OAuthFlowTypeCode}, AllowedOAuthFlowsUserPoolClient: true,
		AllowedOAuthScopes: []string{"openid", "email"}, CallbackURLs: []string{callback}, LogoutURLs: []string{signedOut},
		SupportedIdentityProviders: []string{"COGNITO"},
	})
	require.NoError(t, err)
	clientID, secret := aws.ToString(client.UserPoolClient.ClientId), aws.ToString(client.UserPoolClient.ClientSecret)
	prefix := strings.ToLower(strings.ReplaceAll(uniquename.New("signin"), "_", "-"))
	_, err = c.CreateUserPoolDomain(ctx, &cip.CreateUserPoolDomainInput{UserPoolId: aws.String(poolID), Domain: aws.String(prefix)})
	require.NoError(t, err)
	_, err = c.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId: aws.String(poolID), Username: aws.String("carol"), MessageAction: ciptypes.MessageActionTypeSuppress,
		UserAttributes: []ciptypes.AttributeType{{Name: aws.String("email"), Value: aws.String("carol@example.com")},
			{Name: aws.String("email_verified"), Value: aws.String("true")}},
	})
	require.NoError(t, err)
	_, err = c.AdminSetUserPassword(ctx, &cip.AdminSetUserPasswordInput{
		UserPoolId: aws.String(poolID), Username: aws.String("carol"), Password: aws.String("Sign-In-Pass-42"), Permanent: true,
	})
	require.NoError(t, err)

	base := "https://" + prefix + ".auth.us-east-1.amazoncognito.com"
	browser := cognitoBrowser(t, "app.example.com")
	verifier := "a-code-verifier-of-sufficient-length-for-pkce-0123456789"
	challenge := sha256.Sum256([]byte(verifier))
	authorize := base + "/oauth2/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {callback}, "state": {"xyz"},
		"nonce": {"n-0S6"}, "scope": {"openid email"}, "code_challenge_method": {"S256"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}.Encode()

	resp, err := browser.Get(authorize)
	require.NoError(t, err)
	page := cognitoRead(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, page)
	assert.Equal(t, "/login", resp.Request.URL.Path, "authorize sends a user without a session to the login endpoint")
	loginURL := resp.Request.URL.String()

	resp, err = browser.PostForm(loginURL, url.Values{"username": {"carol"}, "password": {"wrong-Pass-42"}})
	require.NoError(t, err)
	assert.Contains(t, cognitoRead(t, resp), "Incorrect username or password.")

	resp, err = browser.PostForm(loginURL, url.Values{"username": {"carol"}, "password": {"Sign-In-Pass-42"}})
	require.NoError(t, err)
	_ = cognitoRead(t, resp)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	redirect, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "app.example.com", redirect.Host)
	assert.Equal(t, "xyz", redirect.Query().Get("state"))
	code := redirect.Query().Get("code")
	require.NotEmpty(t, code)

	redeem := func(form url.Values) (int, map[string]any) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/oauth2/token", strings.NewReader(form.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(clientID, secret)
		resp, err := browser.Do(req)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(cognitoRead(t, resp)), &body))
		return resp.StatusCode, body
	}
	status, body := redeem(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback},
		"code_verifier": {"not-the-verifier"}})
	require.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", body["error"], "a code verifier that does not hash to the challenge fails")

	resp, err = browser.Get(authorize)
	require.NoError(t, err)
	_ = cognitoRead(t, resp)
	require.Equal(t, http.StatusFound, resp.StatusCode, "the session cookie signs the user in without the login page")
	redirect, err = url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code = redirect.Query().Get("code")
	require.NotEmpty(t, code)
	status, body = redeem(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback},
		"code_verifier": {verifier}})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "Bearer", body["token_type"])
	assert.Equal(t, float64(3600), body["expires_in"])
	status, again := redeem(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback},
		"code_verifier": {verifier}})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", again["error"], "a code redeems once")

	idClaims := cognitoVerifyJWT(t, browser, body["id_token"].(string))
	assert.Equal(t, "https://cognito-idp.us-east-1.amazonaws.com/"+poolID, idClaims["iss"])
	assert.Equal(t, clientID, idClaims["aud"])
	assert.Equal(t, "id", idClaims["token_use"])
	assert.Equal(t, "carol", idClaims["cognito:username"])
	assert.Equal(t, "carol@example.com", idClaims["email"])
	assert.Equal(t, true, idClaims["email_verified"])
	assert.Equal(t, "n-0S6", idClaims["nonce"])
	accessClaims := cognitoVerifyJWT(t, browser, body["access_token"].(string))
	assert.Equal(t, "access", accessClaims["token_use"])
	assert.Equal(t, "openid email", accessClaims["scope"])
	assert.Equal(t, idClaims["sub"], accessClaims["sub"])

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/oauth2/userInfo", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+body["access_token"].(string))
	resp, err = browser.Do(req)
	require.NoError(t, err)
	var info map[string]string
	require.NoError(t, json.Unmarshal([]byte(cognitoRead(t, resp)), &info))
	assert.Equal(t, map[string]string{"sub": idClaims["sub"].(string), "username": "carol", "email": "carol@example.com",
		"email_verified": "true"}, info, "userInfo answers the email scope's attributes, verification as a string")

	status, refreshed := redeem(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {body["refresh_token"].(string)}})
	require.Equal(t, http.StatusOK, status, refreshed)
	assert.NotEmpty(t, refreshed["id_token"])
	assert.Nil(t, refreshed["refresh_token"], "without rotation a refresh returns no new refresh token")

	resp, err = browser.Get(base + "/logout?" + url.Values{"client_id": {clientID}, "logout_uri": {signedOut}}.Encode())
	require.NoError(t, err)
	_ = cognitoRead(t, resp)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, signedOut, resp.Header.Get("Location"))
	resp, err = browser.Get(authorize + "&prompt=none")
	require.NoError(t, err)
	_ = cognitoRead(t, resp)
	redirect, err = url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "login_required", redirect.Query().Get("error"), "sign-out ends the session")

	resp, err = browser.Get(strings.Replace(authorize, "response_type=code", "response_type=token", 1))
	require.NoError(t, err)
	_ = cognitoRead(t, resp)
	redirect, err = url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "unauthorized_client", redirect.Query().Get("error"), "the client allows no implicit grant")

	resp, err = browser.Get(strings.Replace(authorize, url.QueryEscape(callback), url.QueryEscape("https://evil.example.com/"), 1))
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "an unregistered redirect URI gets an error page, not a redirect")
	_ = cognitoRead(t, resp)

	_, err = c.DeleteUserPoolDomain(ctx, &cip.DeleteUserPoolDomainInput{UserPoolId: aws.String(poolID), Domain: aws.String(prefix)})
	require.NoError(t, err)
	_, err = c.DeleteUserPool(ctx, &cip.DeleteUserPoolInput{UserPoolId: aws.String(poolID)})
	require.NoError(t, err)
}

func cognitoSecretHash(secret, username, clientID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(username + clientID))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// TestCognito_APISignInSetsANewPassword signs users in through the API: a
// user created with a temporary password answers NEW_PASSWORD_REQUIRED with a
// password the policy accepts, through InitiateAuth and RespondToAuthChallenge
// with the client's secret hash and through the admin operations; the access
// token reads the user and the refresh token renews it.
func TestCognito_APISignInSetsANewPassword(t *testing.T) {
	t.Parallel()
	c := cognitoClient()
	created, err := c.CreateUserPool(ctx, &cip.CreateUserPoolInput{
		PoolName: aws.String(uniquename.New("api-signin")),
		Policies: &ciptypes.UserPoolPolicyType{PasswordPolicy: &ciptypes.PasswordPolicyType{MinimumLength: aws.Int32(12), RequireNumbers: true}},
	})
	require.NoError(t, err)
	poolID := aws.ToString(created.UserPool.Id)
	client, err := c.CreateUserPoolClient(ctx, &cip.CreateUserPoolClientInput{
		UserPoolId: aws.String(poolID), ClientName: aws.String("api"), GenerateSecret: true,
		ExplicitAuthFlows: []ciptypes.ExplicitAuthFlowsType{
			ciptypes.ExplicitAuthFlowsTypeAllowUserPasswordAuth, ciptypes.ExplicitAuthFlowsTypeAllowAdminUserPasswordAuth,
			ciptypes.ExplicitAuthFlowsTypeAllowRefreshTokenAuth,
		},
	})
	require.NoError(t, err)
	clientID, secret := aws.ToString(client.UserPoolClient.ClientId), aws.ToString(client.UserPoolClient.ClientSecret)
	srpOnly, err := c.CreateUserPoolClient(ctx, &cip.CreateUserPoolClientInput{UserPoolId: aws.String(poolID), ClientName: aws.String("srp")})
	require.NoError(t, err)
	for _, name := range []string{"gina", "hal"} {
		_, err = c.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
			UserPoolId: aws.String(poolID), Username: aws.String(name), TemporaryPassword: aws.String("Temporary-0001"),
			MessageAction:  ciptypes.MessageActionTypeSuppress,
			UserAttributes: []ciptypes.AttributeType{{Name: aws.String("email"), Value: aws.String(name + "@example.com")}},
		})
		require.NoError(t, err)
	}

	_, err = c.InitiateAuth(ctx, &cip.InitiateAuthInput{
		ClientId: srpOnly.UserPoolClient.ClientId, AuthFlow: ciptypes.AuthFlowTypeUserPasswordAuth,
		AuthParameters: map[string]string{"USERNAME": "gina", "PASSWORD": "Temporary-0001"},
	})
	var invalid *ciptypes.InvalidParameterException
	require.ErrorAs(t, err, &invalid, "a client whose ExplicitAuthFlows omit the flow refuses it")
	_, err = c.InitiateAuth(ctx, &cip.InitiateAuthInput{
		ClientId: aws.String(clientID), AuthFlow: ciptypes.AuthFlowTypeUserPasswordAuth,
		AuthParameters: map[string]string{"USERNAME": "gina", "PASSWORD": "Temporary-0001", "SECRET_HASH": "wrong"},
	})
	var notAuthorized *ciptypes.NotAuthorizedException
	require.ErrorAs(t, err, &notAuthorized, "a client with a secret requires its secret hash")

	challenge, err := c.InitiateAuth(ctx, &cip.InitiateAuthInput{
		ClientId: aws.String(clientID), AuthFlow: ciptypes.AuthFlowTypeUserPasswordAuth,
		AuthParameters: map[string]string{"USERNAME": "gina", "PASSWORD": "Temporary-0001", "SECRET_HASH": cognitoSecretHash(secret, "gina", clientID)},
	})
	require.NoError(t, err)
	assert.Equal(t, ciptypes.ChallengeNameTypeNewPasswordRequired, challenge.ChallengeName)
	require.NotEmpty(t, aws.ToString(challenge.Session))
	assert.Nil(t, challenge.AuthenticationResult)

	_, err = c.RespondToAuthChallenge(ctx, &cip.RespondToAuthChallengeInput{
		ClientId: aws.String(clientID), ChallengeName: ciptypes.ChallengeNameTypeNewPasswordRequired, Session: challenge.Session,
		ChallengeResponses: map[string]string{"USERNAME": "gina", "NEW_PASSWORD": "short1", "SECRET_HASH": cognitoSecretHash(secret, "gina", clientID)},
	})
	var weak *ciptypes.InvalidPasswordException
	require.ErrorAs(t, err, &weak)
	answered, err := c.RespondToAuthChallenge(ctx, &cip.RespondToAuthChallengeInput{
		ClientId: aws.String(clientID), ChallengeName: ciptypes.ChallengeNameTypeNewPasswordRequired, Session: challenge.Session,
		ChallengeResponses: map[string]string{"USERNAME": "gina", "NEW_PASSWORD": "Gina-New-Pass-7", "SECRET_HASH": cognitoSecretHash(secret, "gina", clientID)},
	})
	require.NoError(t, err)
	require.NotNil(t, answered.AuthenticationResult)
	assert.NotEmpty(t, aws.ToString(answered.AuthenticationResult.IdToken))
	assert.Equal(t, "Bearer", aws.ToString(answered.AuthenticationResult.TokenType))
	confirmed, err := c.AdminGetUser(ctx, &cip.AdminGetUserInput{UserPoolId: aws.String(poolID), Username: aws.String("gina")})
	require.NoError(t, err)
	assert.Equal(t, ciptypes.UserStatusTypeConfirmed, confirmed.UserStatus)

	me, err := c.GetUser(ctx, &cip.GetUserInput{AccessToken: answered.AuthenticationResult.AccessToken})
	require.NoError(t, err)
	assert.Equal(t, "gina", aws.ToString(me.Username))
	refreshed, err := c.InitiateAuth(ctx, &cip.InitiateAuthInput{
		ClientId: aws.String(clientID), AuthFlow: ciptypes.AuthFlowTypeRefreshTokenAuth,
		AuthParameters: map[string]string{"REFRESH_TOKEN": aws.ToString(answered.AuthenticationResult.RefreshToken),
			"SECRET_HASH": cognitoSecretHash(secret, "gina", clientID)},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(refreshed.AuthenticationResult.AccessToken))
	assert.Empty(t, aws.ToString(refreshed.AuthenticationResult.RefreshToken))

	adminChallenge, err := c.AdminInitiateAuth(ctx, &cip.AdminInitiateAuthInput{
		UserPoolId: aws.String(poolID), ClientId: aws.String(clientID), AuthFlow: ciptypes.AuthFlowTypeAdminUserPasswordAuth,
		AuthParameters: map[string]string{"USERNAME": "hal", "PASSWORD": "Temporary-0001", "SECRET_HASH": cognitoSecretHash(secret, "hal", clientID)},
	})
	require.NoError(t, err)
	require.Equal(t, ciptypes.ChallengeNameTypeNewPasswordRequired, adminChallenge.ChallengeName)
	adminAnswered, err := c.AdminRespondToAuthChallenge(ctx, &cip.AdminRespondToAuthChallengeInput{
		UserPoolId: aws.String(poolID), ClientId: aws.String(clientID), ChallengeName: ciptypes.ChallengeNameTypeNewPasswordRequired,
		Session:            adminChallenge.Session,
		ChallengeResponses: map[string]string{"USERNAME": "hal", "NEW_PASSWORD": "Hal-New-Pass-99", "SECRET_HASH": cognitoSecretHash(secret, "hal", clientID)},
	})
	require.NoError(t, err)
	require.NotNil(t, adminAnswered.AuthenticationResult)
	signedIn, err := c.AdminInitiateAuth(ctx, &cip.AdminInitiateAuthInput{
		UserPoolId: aws.String(poolID), ClientId: aws.String(clientID), AuthFlow: ciptypes.AuthFlowTypeAdminUserPasswordAuth,
		AuthParameters: map[string]string{"USERNAME": "hal", "PASSWORD": "Hal-New-Pass-99", "SECRET_HASH": cognitoSecretHash(secret, "hal", clientID)},
	})
	require.NoError(t, err)
	assert.Empty(t, signedIn.ChallengeName, "a confirmed user signs straight in")
	require.NotNil(t, signedIn.AuthenticationResult)
	_, err = c.GetUser(ctx, &cip.GetUserInput{AccessToken: aws.String("not-a-token")})
	require.ErrorAs(t, err, &notAuthorized)
}
